package monitor

import (
	"strings"
	"testing"
	"time"

	"dmetric.dalang.io/internal/model"
)

type fakeStore struct {
	nextID    int64
	inserted  []model.Alert
	updated   []model.Alert
	resolved  []model.Alert
	acked     []int64
	nextError error
}

func (f *fakeStore) InsertAlert(a *model.Alert) (int64, error) {
	f.nextID++
	f.inserted = append(f.inserted, *a)
	return f.nextID, nil
}

func (f *fakeStore) UpdateAlert(a *model.Alert) error {
	f.updated = append(f.updated, *a)
	return nil
}

func (f *fakeStore) ResolveAlert(a *model.Alert) error {
	f.resolved = append(f.resolved, *a)
	return nil
}

func (f *fakeStore) AckAlert(id int64) error {
	f.acked = append(f.acked, id)
	return nil
}

func snapshot(cpu float64) model.Snapshot {
	return model.Snapshot{
		CPU:       model.CPUStat{Percent: cpu, Cores: 8},
		Memory:    model.MemoryStat{Percent: 40},
		Storage:   model.StorageStat{Percent: 20},
		Disk:      model.DiskIOStat{},
		Network:   model.NetworkStat{},
		Processes: model.ProcessSummary{},
		Alerts:    []model.Alert{},
	}
}

func newTestEngine(t *testing.T, th model.Thresholds) (*AlertEngine, *fakeStore, *time.Time) {
	t.Helper()
	store := &fakeStore{}
	engine := NewAlertEngine(store, th)
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return now }
	return engine, store, &now
}

func TestCPUSustainedAlertAndRecovery(t *testing.T) {
	th := model.DefaultThresholds()
	th.SustainSeconds = 10
	th.RecoverySeconds = 10
	engine, store, now := newTestEngine(t, th)

	if alerts := engine.Evaluate(snapshot(90)); len(alerts) != 0 {
		t.Fatalf("alert fired before the sustain window: %+v", alerts)
	}
	*now = now.Add(9 * time.Second)
	if alerts := engine.Evaluate(snapshot(90)); len(alerts) != 0 {
		t.Fatalf("alert fired at 9s, before 10s: %+v", alerts)
	}
	*now = now.Add(2 * time.Second)
	alerts := engine.Evaluate(snapshot(90))
	if len(alerts) != 1 {
		t.Fatalf("want 1 active alert, got %+v", alerts)
	}
	first := alerts[0]
	if first.Kind != "cpu" || first.ID != 1 {
		t.Fatalf("unexpected alert: %+v", first)
	}
	if first.Severity != "warning" {
		t.Fatalf("severity at 90%% with an 85%% limit = %q, want warning", first.Severity)
	}
	if len(store.inserted) != 1 {
		t.Fatalf("alert was not persisted, inserted=%d", len(store.inserted))
	}

	// A higher value raises the peak and the severity, without creating a
	// second alert.
	*now = now.Add(2 * time.Second)
	alerts = engine.Evaluate(snapshot(99))
	if len(alerts) != 1 || alerts[0].Value != 99 {
		t.Fatalf("peak was not updated: %+v", alerts)
	}
	if alerts[0].Severity != "critical" {
		t.Fatalf("severity at 99%% = %q, want critical", alerts[0].Severity)
	}
	if !strings.Contains(alerts[0].Message, "99.0%") {
		t.Fatalf("message does not show the peak: %q", alerts[0].Message)
	}

	// The condition clears; the alert stays until the recovery window.
	*now = now.Add(2 * time.Second)
	if alerts := engine.Evaluate(snapshot(0)); len(alerts) != 1 {
		t.Fatalf("alert ended immediately: %+v", alerts)
	}
	*now = now.Add(9 * time.Second)
	if alerts := engine.Evaluate(snapshot(0)); len(alerts) != 1 {
		t.Fatalf("alert ended after 9s, before 10s: %+v", alerts)
	}
	*now = now.Add(2 * time.Second)
	if alerts := engine.Evaluate(snapshot(0)); len(alerts) != 0 {
		t.Fatalf("alert did not end after the recovery window: %+v", alerts)
	}
	if len(store.resolved) != 1 {
		t.Fatalf("resolved=%d, want 1", len(store.resolved))
	}
	if store.resolved[0].EndedAt == 0 {
		t.Fatal("resolved alert has no end time")
	}
}

func TestAcknowledgement(t *testing.T) {
	th := model.DefaultThresholds()
	th.SustainSeconds = 5
	engine, store, now := newTestEngine(t, th)

	engine.Evaluate(snapshot(90))
	*now = now.Add(6 * time.Second)
	alerts := engine.Evaluate(snapshot(90))
	if len(alerts) != 1 {
		t.Fatalf("want an alert, got %+v", alerts)
	}
	if err := engine.Ack(alerts[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(store.acked) != 1 || store.acked[0] != alerts[0].ID {
		t.Fatalf("store acks = %v", store.acked)
	}
	alerts = engine.Evaluate(snapshot(90))
	if len(alerts) != 1 || !alerts[0].Acked {
		t.Fatalf("active alert is not marked as read: %+v", alerts)
	}
}

func TestProcessAlerts(t *testing.T) {
	th := model.DefaultThresholds()
	th.SustainSeconds = 5
	th.RecoverySeconds = 5
	th.ProcessCPUPercent = 80
	th.ProcessMemoryMB = 1024
	engine, store, now := newTestEngine(t, th)

	snap := snapshot(1)
	snap.AllProcesses = []model.ProcessInfo{
		{PID: 42, Name: "pemakan", CPUPercent: 95},
		{PID: 43, Name: "ram-hog", MemoryRSS: 2 << 30},
	}
	engine.Evaluate(snap)
	*now = now.Add(4 * time.Second)
	if alerts := engine.Evaluate(snap); len(alerts) != 0 {
		t.Fatalf("process alerts fired early: %+v", alerts)
	}
	*now = now.Add(2 * time.Second)
	alerts := engine.Evaluate(snap)
	if len(alerts) != 2 {
		t.Fatalf("want CPU and memory process alerts, got %+v", alerts)
	}
	foundCPU, foundRAM := false, false
	for _, a := range alerts {
		if a.Kind == "process_cpu" && strings.Contains(a.Message, "pemakan") {
			foundCPU = true
		}
		if a.Kind == "process_memory" && strings.Contains(a.Message, "ram-hog") {
			foundRAM = true
		}
	}
	if !foundCPU || !foundRAM {
		t.Fatalf("missing process alerts: %+v", alerts)
	}

	// The process exits; both alerts recover.
	snap.AllProcesses = nil
	*now = now.Add(2 * time.Second)
	engine.Evaluate(snap)
	*now = now.Add(6 * time.Second)
	if alerts := engine.Evaluate(snap); len(alerts) != 0 {
		t.Fatalf("process alerts did not recover: %+v", alerts)
	}
	if len(store.resolved) != 2 {
		t.Fatalf("resolved=%d, want 2", len(store.resolved))
	}
}

func TestPerCoreSlowdownAlert(t *testing.T) {
	th := model.DefaultThresholds()
	th.SustainSeconds = 5
	engine, _, now := newTestEngine(t, th)

	snap := snapshot(70)
	snap.CPU.LoadSupported = true
	snap.CPU.Cores = 4
	snap.CPU.Load1 = 8 // 2 per core, limit 1.5
	engine.Evaluate(snap)
	*now = now.Add(6 * time.Second)
	alerts := engine.Evaluate(snap)
	if len(alerts) != 1 || alerts[0].Kind != "load" {
		t.Fatalf("want a slowdown alert, got %+v", alerts)
	}
	if !strings.Contains(alerts[0].Message, "melambat") {
		t.Fatalf("unexpected slowdown message: %q", alerts[0].Message)
	}
}

func TestThresholdChangeClearsAlert(t *testing.T) {
	th := model.DefaultThresholds()
	th.SustainSeconds = 5
	th.RecoverySeconds = 5
	engine, _, now := newTestEngine(t, th)

	engine.Evaluate(snapshot(90))
	*now = now.Add(6 * time.Second)
	if alerts := engine.Evaluate(snapshot(90)); len(alerts) != 1 {
		t.Fatalf("want an alert, got %+v", alerts)
	}

	raised := th
	raised.CPUPercent = 99
	engine.SetThresholds(raised)
	*now = now.Add(1 * time.Second)
	if alerts := engine.Evaluate(snapshot(90)); len(alerts) != 1 {
		t.Fatalf("alert ended before the recovery window: %+v", alerts)
	}
	*now = now.Add(6 * time.Second)
	if alerts := engine.Evaluate(snapshot(90)); len(alerts) != 0 {
		t.Fatalf("alert survived the raised threshold: %+v", alerts)
	}
}
