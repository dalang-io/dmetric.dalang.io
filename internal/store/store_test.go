package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"dmetric.dalang.io/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dmetric.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSettingsRoundTrip(t *testing.T) {
	s := openTestStore(t)

	settings := s.LoadSettings()
	if settings.Port != model.DefaultPort {
		t.Fatalf("fresh settings port = %d, want %d", settings.Port, model.DefaultPort)
	}
	settings.Port = 9001
	settings.IntervalSeconds = 5
	settings.Thresholds.CPUPercent = 70
	settings.WatchApps = []string{"chromeleon", "sqlservr"}
	settings.NetworkProbes = []string{"127.0.0.1:1433"}
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	loaded := s.LoadSettings()
	if loaded.Port != 9001 || loaded.IntervalSeconds != 5 || loaded.Thresholds.CPUPercent != 70 {
		t.Fatalf("round trip mismatch: %+v", loaded)
	}
	if len(loaded.WatchApps) != 2 || len(loaded.NetworkProbes) != 1 {
		t.Fatalf("watch apps/probes did not round trip: %+v", loaded)
	}
}

func TestHistoryAndThinning(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()
	for i := range 20 {
		err := s.InsertSample(model.HistoryPoint{
			Ts:           now.Add(-time.Duration(20-i) * time.Second).UnixMilli(),
			CPU:          float64(i),
			RAM:          50,
			Procs:        100 + i,
			DiskReadLat:  1.5,
			DiskWriteLat: 2.5,
			DiskQueue:    3,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	points, err := s.History(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 20 {
		t.Fatalf("history returned %d points, want 20", len(points))
	}
	if points[0].CPU != 0 || points[19].CPU != 19 {
		t.Fatalf("history order is wrong: first=%v last=%v", points[0].CPU, points[19].CPU)
	}
	if points[0].DiskWriteLat != 2.5 || points[0].DiskQueue != 3 {
		t.Fatalf("latency history did not round trip: %+v", points[0])
	}

	many := make([]model.HistoryPoint, 100)
	for i := range many {
		many[i] = model.HistoryPoint{Ts: int64(i), CPU: float64(i)}
	}
	narrow := thin(many, 10)
	if len(narrow) != 10 {
		t.Fatalf("thin returned %d points, want 10", len(narrow))
	}
	if narrow[0].CPU != 4.5 {
		t.Fatalf("first bucket average = %v, want 4.5", narrow[0].CPU)
	}
}

func TestAlertsLifecycle(t *testing.T) {
	s := openTestStore(t)
	now := model.NowMillis()
	a := &model.Alert{
		Key: "system:cpu", Kind: "cpu", Severity: "warning",
		Message: "CPU tinggi", Value: 90, Threshold: 85,
		StartedAt: now, UpdatedAt: now,
	}
	id, err := s.InsertAlert(a)
	if err != nil {
		t.Fatal(err)
	}
	if id < 1 {
		t.Fatalf("id = %d", id)
	}

	alerts, err := s.ListAlerts(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].ID != id || alerts[0].EndedAt != 0 {
		t.Fatalf("unexpected alerts: %+v", alerts)
	}

	if err := s.AckAlert(id); err != nil {
		t.Fatal(err)
	}
	alerts, _ = s.ListAlerts(10)
	if !alerts[0].Acked {
		t.Fatal("alert was not acknowledged")
	}

	a.ID = id
	a.Value = 99
	a.Severity = "critical"
	a.EndedAt = now + 5000
	a.UpdatedAt = a.EndedAt
	if err := s.ResolveAlert(a); err != nil {
		t.Fatal(err)
	}
	alerts, _ = s.ListAlerts(10)
	if alerts[0].EndedAt != a.EndedAt || alerts[0].Value != 99 {
		t.Fatalf("alert was not resolved: %+v", alerts[0])
	}

	// Alerts left open by a crash are closed at the next start.
	open := &model.Alert{
		Key: "system:memory", Kind: "memory", Severity: "warning",
		Message: "RAM tinggi", Value: 90, Threshold: 85,
		StartedAt: now + 6000, UpdatedAt: now + 6000,
	}
	openID, err := s.InsertAlert(open)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CloseOpenAlerts(now + 10000); err != nil {
		t.Fatal(err)
	}
	alerts, _ = s.ListAlerts(10)
	for _, got := range alerts {
		if got.ID == openID && got.EndedAt == 0 {
			t.Fatal("open alert was not closed")
		}
	}
}

func TestPrune(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)
	if err := s.InsertSample(model.HistoryPoint{Ts: old.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSample(model.HistoryPoint{Ts: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(now); err != nil {
		t.Fatal(err)
	}
	points, err := s.History(24 * 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("prune left %d points, want 1", len(points))
	}
}

func TestMigrateLatencyColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE samples (
		ts INTEGER PRIMARY KEY,
		cpu REAL NOT NULL, ram REAL NOT NULL,
		disk_read REAL NOT NULL, disk_write REAL NOT NULL,
		net_rx REAL NOT NULL, net_tx REAL NOT NULL,
		load1 REAL NOT NULL, ports INTEGER NOT NULL, procs INTEGER NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	_, err = db.Exec(`INSERT INTO samples VALUES (?, 10, 20, 0, 0, 0, 0, 0, 1, 2)`, now-1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening an old database: %v", err)
	}
	defer s.Close()
	if err := s.InsertSample(model.HistoryPoint{
		Ts: now, CPU: 11, DiskReadLat: 4, DiskWriteLat: 5, DiskQueue: 2,
	}); err != nil {
		t.Fatalf("insert with latency: %v", err)
	}
	points, err := s.History(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("history returned %d points, want 2", len(points))
	}
	last := points[len(points)-1]
	if last.DiskReadLat != 4 || last.DiskWriteLat != 5 || last.DiskQueue != 2 {
		t.Fatalf("migrated latency columns not readable: %+v", last)
	}
	if points[0].DiskReadLat != 0 {
		t.Fatalf("old row should default to zero latency: %+v", points[0])
	}
}

func TestBaseline(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()
	for i := range 100 {
		err := s.InsertSample(model.HistoryPoint{
			Ts:           now.Add(-time.Duration(100-i) * time.Second).UnixMilli(),
			CPU:          float64(i),
			RAM:          50,
			DiskWriteLat: float64(i) / 10,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.Baseline(60)
	if err != nil {
		t.Fatal(err)
	}
	if b.Samples != 100 {
		t.Fatalf("samples = %d, want 100", b.Samples)
	}
	if b.CPU50 != 49 || b.CPU95 != 94 {
		t.Fatalf("percentiles: p50=%v p95=%v", b.CPU50, b.CPU95)
	}
	if b.RAM50 != 50 || b.DiskWriteLat95 != 9.4 {
		t.Fatalf("unexpected baseline: %+v", b)
	}
}

func TestSettingsNetworkInterfacesRoundTrip(t *testing.T) {
	s := openTestStore(t)
	settings := s.LoadSettings()
	settings.NetworkInterfaces = []string{"Ethernet", "Ethernet 2"}
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	loaded := s.LoadSettings()
	if len(loaded.NetworkInterfaces) != 2 || loaded.NetworkInterfaces[0] != "Ethernet" {
		t.Fatalf("network interfaces did not round trip: %+v", loaded.NetworkInterfaces)
	}
}
