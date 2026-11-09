// Package monitor samples the machine, evaluates alert thresholds, persists
// the samples and broadcasts them to the web UI.
package monitor

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"dmetric.dalang.io/internal/hardware"
	"dmetric.dalang.io/internal/model"
)

// Monitor owns the collector, the alert engine and the live subscribers.
type Monitor struct {
	col      *Collector
	store    storeWriter
	engine   *AlertEngine
	interval time.Duration
	start    time.Time

	mu                sync.RWMutex
	latest            model.Snapshot
	subs              map[chan model.Snapshot]struct{}
	nextPrun          time.Time
	recent            []model.HistoryPoint
	watchApps         []string
	baseline          model.Baseline
	nextBaseline      time.Time
	hardware          model.Hardware
	networkInterfaces []string
	prober            *networkProber
	proberMu          sync.RWMutex
}

type storeWriter interface {
	InsertSample(model.HistoryPoint) error
	Prune(time.Time) error
	Baseline(minutes int) (model.Baseline, error)
}

// New creates a monitor and takes the first sample right away, so the API
// has data as soon as the HTTP server starts.
func New(store storeWriter, alertStore alertStore, interval time.Duration, th model.Thresholds,
	watchApps, networkProbes, networkInterfaces []string) *Monitor {
	if interval <= 0 {
		interval = time.Duration(model.DefaultIntervalSeconds) * time.Second
	}
	m := &Monitor{
		col:               NewCollector(),
		store:             store,
		engine:            NewAlertEngine(alertStore, th),
		interval:          interval,
		start:             time.Now(),
		subs:              map[chan model.Snapshot]struct{}{},
		nextPrun:          time.Now().Add(time.Hour),
		watchApps:         sanitizePatterns(watchApps),
		networkInterfaces: sanitizePatterns(networkInterfaces),
		prober:            newNetworkProber(networkProbes),
		hardware: model.Hardware{
			Status:       "unknown",
			Accelerators: []model.HWAccelerator{},
			Fans:         []model.HWFan{},
			PSUs:         []model.HWPSU{},
			Storage:      []model.HWStorage{},
			Issues:       []string{},
			Notes:        []string{"Mengumpulkan kesehatan hardware…"},
		},
	}
	m.sample(context.Background())
	return m
}

// Run samples every interval until the context is canceled.
func (m *Monitor) Run(ctx context.Context) {
	go m.hardwareLoop(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			sampleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			m.sample(sampleCtx)
			cancel()
			if now.After(m.nextPrun) {
				if err := m.store.Prune(now); err != nil {
					log.Printf("dmetric: membersihkan data lama: %v", err)
				}
				m.nextPrun = now.Add(time.Hour)
			}
		}
	}
}

func (m *Monitor) sample(ctx context.Context) {
	snap := m.col.Sample(ctx)

	m.mu.RLock()
	patterns := append([]string(nil), m.watchApps...)
	m.mu.RUnlock()
	snap.Apps = appStats(snap.AllProcesses, patterns)

	m.proberMu.RLock()
	prober := m.prober
	m.proberMu.RUnlock()
	snap.Network.Gateway, snap.Network.Probes = prober.probe(ctx)
	m.applyNetworkInterfaces(&snap)

	now := time.Now()
	if m.nextBaseline.IsZero() || now.After(m.nextBaseline) {
		if baseline, err := m.store.Baseline(60); err == nil {
			m.baseline = baseline
		} else {
			log.Printf("dmetric: baseline: %v", err)
		}
		m.nextBaseline = now.Add(5 * time.Minute)
	}
	snap.Baseline = m.baseline

	point := model.HistoryPoint{
		Ts:        snap.Ts,
		CPU:       snap.CPU.Percent,
		RAM:       snap.Memory.Percent,
		DiskRead:  snap.Disk.ReadBytesPerSec,
		DiskWrite: snap.Disk.WriteBytesPerSec,
		NetRx:     snap.Network.RxBytesPerSec,
		NetTx:     snap.Network.TxBytesPerSec,
		Load1:     snap.CPU.Load1,
		Ports:     snap.Ports.Count,
		Procs:     snap.Processes.Count,
	}
	point.DiskReadLat = snap.Disk.ReadLatencyMs
	point.DiskWriteLat = snap.Disk.WriteLatencyMs
	point.DiskQueue = snap.Disk.Queue
	for _, volume := range snap.Storage.Volumes {
		if !volume.HasIO {
			continue
		}
		if volume.ReadLatencyMs > point.DiskReadLat {
			point.DiskReadLat = volume.ReadLatencyMs
		}
		if volume.WriteLatencyMs > point.DiskWriteLat {
			point.DiskWriteLat = volume.WriteLatencyMs
		}
		if volume.Queue > point.DiskQueue {
			point.DiskQueue = volume.Queue
		}
	}
	m.mu.Lock()
	hardwareSnapshot := m.hardware
	m.recent = append(m.recent, point)
	if len(m.recent) > 90 {
		m.recent = m.recent[len(m.recent)-90:]
	}
	recent := append([]model.HistoryPoint(nil), m.recent...)
	m.mu.Unlock()

	snap.Hardware = hardwareSnapshot
	snap.Diagnosis = diagnose(snap, recent, m.engine.Thresholds())
	snap.Alerts = m.engine.Evaluate(snap)

	if err := m.store.InsertSample(point); err != nil {
		log.Printf("dmetric: menyimpan sampel: %v", err)
	}

	m.mu.Lock()
	m.latest = snap
	subs := make([]chan model.Snapshot, 0, len(m.subs))
	for ch := range m.subs {
		subs = append(subs, ch)
	}
	m.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- snap:
		default:
			// The subscriber is behind; the next sample carries everything.
		}
	}
}

func (m *Monitor) hardwareLoop(ctx context.Context) {
	m.refreshHardware(ctx)
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.refreshHardware(ctx)
		}
	}
}

func (m *Monitor) refreshHardware(ctx context.Context) {
	runCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	hw := hardware.Collect(runCtx)
	cancel()
	m.mu.Lock()
	m.hardware = hw
	m.mu.Unlock()
}

// Latest returns the newest snapshot.
func (m *Monitor) Latest() model.Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.latest
}

// Subscribe returns a channel of snapshots and a func that unsubscribes.
func (m *Monitor) Subscribe() (<-chan model.Snapshot, func()) {
	ch := make(chan model.Snapshot, 2)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.subs, ch)
		m.mu.Unlock()
	}
}

// SetThresholds applies new alert limits immediately.
func (m *Monitor) SetThresholds(th model.Thresholds) { m.engine.SetThresholds(th) }

// SetNetworkProbes replaces the TCP probe targets immediately.
func (m *Monitor) SetNetworkProbes(targets []string) {
	prober := newNetworkProber(targets)
	m.proberMu.Lock()
	m.prober = prober
	m.proberMu.Unlock()
}

// SetNetworkInterfaces selects which interfaces make up the Internet card.
// An empty list means all non-loopback interfaces.
func (m *Monitor) SetNetworkInterfaces(names []string) {
	cleaned := sanitizePatterns(names)
	m.mu.Lock()
	m.networkInterfaces = cleaned
	m.mu.Unlock()
}

func (m *Monitor) applyNetworkInterfaces(snap *model.Snapshot) {
	m.mu.RLock()
	selected := append([]string(nil), m.networkInterfaces...)
	m.mu.RUnlock()

	if len(snap.Network.Interfaces) == 0 {
		return
	}
	if len(selected) == 0 {
		for i := range snap.Network.Interfaces {
			snap.Network.Interfaces[i].Selected = true
		}
		return
	}
	wanted := make(map[string]bool, len(selected))
	for _, name := range selected {
		wanted[strings.ToLower(name)] = true
	}
	var rx, tx float64
	var rxTotal, txTotal uint64
	matched := false
	for i := range snap.Network.Interfaces {
		iface := &snap.Network.Interfaces[i]
		iface.Selected = wanted[strings.ToLower(iface.Name)]
		if !iface.Selected {
			continue
		}
		matched = true
		rx += iface.RxBytesPerSec
		tx += iface.TxBytesPerSec
		rxTotal += iface.RxBytesTotal
		txTotal += iface.TxBytesTotal
	}
	if !matched {
		// A renamed/removed NIC should not blank the card.
		snap.Warnings = append(snap.Warnings, "interface jaringan terpilih tidak ditemukan; memakai semua interface")
		for i := range snap.Network.Interfaces {
			iface := &snap.Network.Interfaces[i]
			iface.Selected = true
			rx += iface.RxBytesPerSec
			tx += iface.TxBytesPerSec
			rxTotal += iface.RxBytesTotal
			txTotal += iface.TxBytesTotal
		}
	}
	snap.Network.RxBytesPerSec = rx
	snap.Network.TxBytesPerSec = tx
	snap.Network.RxBytesTotal = rxTotal
	snap.Network.TxBytesTotal = txTotal
}

// SetWatchApps changes the focus-application patterns from the next sample.
func (m *Monitor) SetWatchApps(patterns []string) {
	cleaned := sanitizePatterns(patterns)
	m.mu.Lock()
	m.watchApps = cleaned
	m.mu.Unlock()
}

// WatchApps returns the active focus-application patterns.
func (m *Monitor) WatchApps() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string(nil), m.watchApps...)
}

func sanitizePatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	seen := map[string]bool{}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		key := strings.ToLower(pattern)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, pattern)
	}
	return out
}

// Thresholds returns the active alert limits.
func (m *Monitor) Thresholds() model.Thresholds { return m.engine.Thresholds() }

// Interval returns the sampling interval.
func (m *Monitor) Interval() time.Duration { return m.interval }

// Start returns when the monitor started.
func (m *Monitor) Start() time.Time { return m.start }

// Ack marks an alert as acknowledged, including the alert in the latest
// snapshot so a client that reads it right away sees the change.
func (m *Monitor) Ack(id int64) error {
	if err := m.engine.Ack(id); err != nil {
		return err
	}
	m.mu.Lock()
	// Copy-on-write: snapshots already handed to /api/snapshot or
	// subscribers may share this backing array.
	alerts := append([]model.Alert(nil), m.latest.Alerts...)
	for i := range alerts {
		if alerts[i].ID == id {
			alerts[i].Acked = true
		}
	}
	m.latest.Alerts = alerts
	m.mu.Unlock()
	return nil
}
