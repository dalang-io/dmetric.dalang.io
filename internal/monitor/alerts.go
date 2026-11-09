package monitor

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"dmetric.dalang.io/internal/model"
)

// alertStore is the subset of store.Store the engine needs. It is an
// interface so the engine can be tested without a database.
type alertStore interface {
	InsertAlert(*model.Alert) (int64, error)
	UpdateAlert(*model.Alert) error
	ResolveAlert(*model.Alert) error
	AckAlert(int64) error
}

type alertState struct {
	aboveSince time.Time
	belowSince time.Time
	active     *model.Alert
	lastStore  time.Time
	lastSev    string
}

type candidate struct {
	key       string
	kind      string
	severity  string
	message   string
	value     float64
	threshold float64
}

// AlertEngine turns threshold breaches that last for the configured time
// into alerts, and closes them after the condition has been below the
// threshold for the recovery time.
type AlertEngine struct {
	mu     sync.Mutex
	th     model.Thresholds
	store  alertStore
	now    func() time.Time
	states map[string]*alertState
}

// NewAlertEngine creates an engine with no active alerts.
func NewAlertEngine(store alertStore, th model.Thresholds) *AlertEngine {
	th.Normalize()
	return &AlertEngine{
		th:     th,
		store:  store,
		now:    time.Now,
		states: map[string]*alertState{},
	}
}

// SetThresholds replaces the limits; changes apply from the next sample.
func (e *AlertEngine) SetThresholds(th model.Thresholds) {
	th.Normalize()
	e.mu.Lock()
	e.th = th
	e.mu.Unlock()
}

// Thresholds returns the current limits.
func (e *AlertEngine) Thresholds() model.Thresholds {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.th
}

// Evaluate updates the alert state with one snapshot and returns the
// alerts that are active now.
func (e *AlertEngine) Evaluate(snap model.Snapshot) []model.Alert {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.now()
	sustain := time.Duration(e.th.SustainSeconds) * time.Second
	recovery := time.Duration(e.th.RecoverySeconds) * time.Second
	cands := e.candidates(snap)
	seen := make(map[string]bool, len(cands))

	for key, cand := range cands {
		seen[key] = true
		st := e.states[key]
		if st == nil {
			st = &alertState{}
			e.states[key] = st
		}
		st.belowSince = time.Time{}
		if st.aboveSince.IsZero() {
			st.aboveSince = now
		}
		if st.active == nil {
			if now.Sub(st.aboveSince) < sustain {
				continue
			}
			alert := model.Alert{
				Key:       cand.key,
				Kind:      cand.kind,
				Severity:  cand.severity,
				Message:   cand.message,
				Value:     cand.value,
				Threshold: cand.threshold,
				StartedAt: now.UnixMilli(),
				UpdatedAt: now.UnixMilli(),
			}
			if id, err := e.store.InsertAlert(&alert); err == nil {
				alert.ID = id
			}
			st.active = &alert
			st.lastStore = now
			st.lastSev = alert.Severity
			continue
		}

		alert := st.active
		alert.UpdatedAt = now.UnixMilli()
		if cand.value > alert.Value {
			alert.Value = cand.value
			alert.Message = cand.message
			alert.Severity = cand.severity
			alert.Threshold = cand.threshold
		} else if cand.threshold != alert.Threshold {
			alert.Threshold = cand.threshold
			alert.Message = cand.message
			alert.Severity = cand.severity
		}
		if alert.Severity != st.lastSev || now.Sub(st.lastStore) >= 15*time.Second {
			_ = e.store.UpdateAlert(alert)
			st.lastStore = now
			st.lastSev = alert.Severity
		}
	}

	for key, st := range e.states {
		if seen[key] {
			continue
		}
		st.aboveSince = time.Time{}
		if st.active == nil {
			delete(e.states, key)
			continue
		}
		if st.belowSince.IsZero() {
			st.belowSince = now
		}
		st.active.UpdatedAt = now.UnixMilli()
		if now.Sub(st.belowSince) >= recovery {
			st.active.EndedAt = now.UnixMilli()
			_ = e.store.ResolveAlert(st.active)
			delete(e.states, key)
		}
	}

	return e.activeLocked()
}

// Ack marks an alert as acknowledged, including one that already ended.
func (e *AlertEngine) Ack(id int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, st := range e.states {
		if st.active != nil && st.active.ID == id {
			st.active.Acked = true
		}
	}
	return e.store.AckAlert(id)
}

func (e *AlertEngine) activeLocked() []model.Alert {
	out := make([]model.Alert, 0, len(e.states))
	for _, st := range e.states {
		if st.active != nil {
			out = append(out, *st.active)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out
}

func (e *AlertEngine) candidates(s model.Snapshot) map[string]candidate {
	th := e.th
	out := map[string]candidate{}

	add := func(key, kind, message string, value, threshold float64) {
		out[key] = candidate{
			key:       key,
			kind:      kind,
			severity:  severity(kind, value, threshold),
			message:   message,
			value:     value,
			threshold: threshold,
		}
	}

	if s.CPU.Percent >= th.CPUPercent {
		add("system:cpu", "cpu",
			fmt.Sprintf("CPU tinggi: %.1f%% (ambang %.0f%%)", s.CPU.Percent, th.CPUPercent),
			s.CPU.Percent, th.CPUPercent)
	}
	if s.Memory.Percent >= th.MemoryPercent {
		add("system:memory", "memory",
			fmt.Sprintf("RAM tinggi: %.1f%% terpakai (ambang %.0f%%)", s.Memory.Percent, th.MemoryPercent),
			s.Memory.Percent, th.MemoryPercent)
	}
	if s.Storage.Percent >= th.StoragePercent {
		add("system:storage", "storage",
			fmt.Sprintf("Penyimpanan hampir penuh: %.1f%% terpakai (ambang %.0f%%)", s.Storage.Percent, th.StoragePercent),
			s.Storage.Percent, th.StoragePercent)
	}
	readMB := s.Disk.ReadBytesPerSec / 1e6
	if readMB >= th.DiskReadMBps {
		add("system:disk_read", "disk_read",
			fmt.Sprintf("Baca disk tinggi: %s (ambang %.0f MB/s)", model.FormatRate(s.Disk.ReadBytesPerSec), th.DiskReadMBps),
			readMB, th.DiskReadMBps)
	}
	writeMB := s.Disk.WriteBytesPerSec / 1e6
	if writeMB >= th.DiskWriteMBps {
		add("system:disk_write", "disk_write",
			fmt.Sprintf("Tulis disk tinggi: %s (ambang %.0f MB/s)", model.FormatRate(s.Disk.WriteBytesPerSec), th.DiskWriteMBps),
			writeMB, th.DiskWriteMBps)
	}
	rxMB := s.Network.RxBytesPerSec / 1e6
	if rxMB >= th.NetRxMBps {
		add("system:net_rx", "net_rx",
			fmt.Sprintf("Unduh jaringan tinggi: %s (ambang %.0f MB/s)", model.FormatRate(s.Network.RxBytesPerSec), th.NetRxMBps),
			rxMB, th.NetRxMBps)
	}
	txMB := s.Network.TxBytesPerSec / 1e6
	if txMB >= th.NetTxMBps {
		add("system:net_tx", "net_tx",
			fmt.Sprintf("Unggah jaringan tinggi: %s (ambang %.0f MB/s)", model.FormatRate(s.Network.TxBytesPerSec), th.NetTxMBps),
			txMB, th.NetTxMBps)
	}
	if s.CPU.LoadSupported && s.CPU.Cores > 0 {
		perCore := s.CPU.Load1 / float64(s.CPU.Cores)
		if perCore >= th.LoadPerCore {
			add("system:load", "load",
				fmt.Sprintf("Sistem melambat: load 1m %.2f (%.2f per core, ambang %.2f)", s.CPU.Load1, perCore, th.LoadPerCore),
				perCore, th.LoadPerCore)
		}
	}

	for _, p := range s.AllProcesses {
		name := p.Name
		if name == "" {
			name = fmt.Sprintf("pid %d", p.PID)
		}
		if p.CPUPercent >= th.ProcessCPUPercent {
			add(fmt.Sprintf("process:cpu:%d:%s", p.PID, name), "process_cpu",
				fmt.Sprintf("Proses %s (PID %d) memakai CPU %.1f%% (ambang %.0f%%)", name, p.PID, p.CPUPercent, th.ProcessCPUPercent),
				p.CPUPercent, th.ProcessCPUPercent)
		}
		memMB := float64(p.MemoryRSS) / (1024 * 1024)
		if memMB >= th.ProcessMemoryMB {
			add(fmt.Sprintf("process:memory:%d:%s", p.PID, name), "process_memory",
				fmt.Sprintf("Proses %s (PID %d) memakai RAM %s (ambang %.0f MB)", name, p.PID, model.FormatBytes(float64(p.MemoryRSS)), th.ProcessMemoryMB),
				memMB, th.ProcessMemoryMB)
		}
	}
	return out
}

// severity marks usage metrics critical from 95% (or 1.25x their limit
// when the limit is lower), and throughput, load and memory metrics from
// 1.25x their limit.
func severity(kind string, value, threshold float64) string {
	switch kind {
	case "cpu", "memory", "storage", "process_cpu":
		if value >= 95 || value >= threshold*1.25 {
			return "critical"
		}
	default:
		if value >= threshold*1.25 {
			return "critical"
		}
	}
	return "warning"
}
