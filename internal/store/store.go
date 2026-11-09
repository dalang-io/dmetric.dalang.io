// Package store persists samples, alerts and settings in SQLite. It uses
// modernc.org/sqlite, which is pure Go, so the app builds without cgo for
// every platform MyGo supports.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"dmetric.dalang.io/internal/model"
)

const (
	settingsKey     = "settings"
	sampleRetention = 7 * 24 * time.Hour
	alertRetention  = 30 * 24 * time.Hour
	historyMaxRows  = 900
)

// Store owns the SQLite database.
type Store struct {
	db   *sql.DB
	path string
}

// Open creates the data directory if needed and opens the database.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("membuat direktori data: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("membuka database: %w", err)
	}
	// One connection serializes writes and keeps SQLite's locking simple;
	// the API only reads small ranges.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("membuka database: %w", err)
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS samples (
	ts         INTEGER PRIMARY KEY,
	cpu        REAL NOT NULL,
	ram        REAL NOT NULL,
	disk_read  REAL NOT NULL,
	disk_write REAL NOT NULL,
	net_rx     REAL NOT NULL,
	net_tx     REAL NOT NULL,
	load1      REAL NOT NULL,
	ports      INTEGER NOT NULL,
	procs      INTEGER NOT NULL,
	disk_read_latency  REAL NOT NULL DEFAULT 0,
	disk_write_latency REAL NOT NULL DEFAULT 0,
	disk_queue         REAL NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS alerts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	key        TEXT NOT NULL,
	kind       TEXT NOT NULL,
	severity   TEXT NOT NULL,
	message    TEXT NOT NULL,
	value      REAL NOT NULL,
	threshold  REAL NOT NULL,
	started_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	ended_at   INTEGER NOT NULL DEFAULT 0,
	acked      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS alerts_ended ON alerts(ended_at);
CREATE INDEX IF NOT EXISTS alerts_started ON alerts(started_at);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrasi database: %w", err)
	}
	// Databases created before latency history existed get the columns
	// added in place; existing rows read as zero.
	columns := []struct{ name, decl string }{
		{"disk_read_latency", "REAL NOT NULL DEFAULT 0"},
		{"disk_write_latency", "REAL NOT NULL DEFAULT 0"},
		{"disk_queue", "REAL NOT NULL DEFAULT 0"},
	}
	for _, column := range columns {
		_, err := s.db.Exec("ALTER TABLE samples ADD COLUMN " + column.name + " " + column.decl)
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
			return fmt.Errorf("migrasi samples.%s: %w", column.name, err)
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// LoadSettings reads the settings, filling in defaults for keys that are
// missing or unreadable.
func (s *Store) LoadSettings() model.Settings {
	settings := model.DefaultSettings()
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, settingsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return settings
	}
	if err != nil {
		log.Printf("dmetric: membaca pengaturan: %v", err)
		return settings
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		log.Printf("dmetric: pengaturan rusak, memakai bawaan: %v", err)
		return model.DefaultSettings()
	}
	settings.Normalize()
	if err := settings.Validate(); err != nil {
		log.Printf("dmetric: pengaturan tidak valid, memakai bawaan: %v", err)
		return model.DefaultSettings()
	}
	return settings
}

// SaveSettings writes the settings as JSON.
func (s *Store) SaveSettings(settings model.Settings) error {
	settings.Normalize()
	if err := settings.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, settingsKey, string(raw))
	return err
}

// InsertSample persists one sample for the charts.
func (s *Store) InsertSample(p model.HistoryPoint) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO samples
		(ts, cpu, ram, disk_read, disk_write, net_rx, net_tx, load1, ports, procs,
		 disk_read_latency, disk_write_latency, disk_queue)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Ts, p.CPU, p.RAM, p.DiskRead, p.DiskWrite, p.NetRx, p.NetTx, p.Load1, p.Ports, p.Procs,
		p.DiskReadLat, p.DiskWriteLat, p.DiskQueue)
	return err
}

// History returns the samples of the last minutes, oldest first, thinned to
// at most historyMaxRows points so the browser gets a small response.
func (s *Store) History(minutes int) ([]model.HistoryPoint, error) {
	if minutes < 1 {
		minutes = 1
	}
	if minutes > 24*60 {
		minutes = 24 * 60
	}
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute).UnixMilli()
	rows, err := s.db.Query(`SELECT ts, cpu, ram, disk_read, disk_write, net_rx, net_tx, load1, ports, procs,
		disk_read_latency, disk_write_latency, disk_queue
		FROM samples WHERE ts >= ? ORDER BY ts`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	points := make([]model.HistoryPoint, 0, 256)
	for rows.Next() {
		var p model.HistoryPoint
		if err := rows.Scan(&p.Ts, &p.CPU, &p.RAM, &p.DiskRead, &p.DiskWrite,
			&p.NetRx, &p.NetTx, &p.Load1, &p.Ports, &p.Procs,
			&p.DiskReadLat, &p.DiskWriteLat, &p.DiskQueue); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return thin(points, historyMaxRows), nil
}

// thin averages consecutive buckets of points when there are too many for
// the charts.
func thin(points []model.HistoryPoint, max int) []model.HistoryPoint {
	if len(points) <= max {
		return points
	}
	bucket := (len(points) + max - 1) / max
	out := make([]model.HistoryPoint, 0, max)
	for i := 0; i < len(points); i += bucket {
		end := i + bucket
		if end > len(points) {
			end = len(points)
		}
		n := float64(end - i)
		avg := model.HistoryPoint{Ts: points[end-1].Ts}
		for _, p := range points[i:end] {
			avg.CPU += p.CPU
			avg.RAM += p.RAM
			avg.DiskRead += p.DiskRead
			avg.DiskWrite += p.DiskWrite
			avg.NetRx += p.NetRx
			avg.NetTx += p.NetTx
			avg.Load1 += p.Load1
			avg.Ports += p.Ports
			avg.Procs += p.Procs
			avg.DiskReadLat += p.DiskReadLat
			avg.DiskWriteLat += p.DiskWriteLat
			avg.DiskQueue += p.DiskQueue
		}
		avg.CPU /= n
		avg.RAM /= n
		avg.DiskRead /= n
		avg.DiskWrite /= n
		avg.NetRx /= n
		avg.NetTx /= n
		avg.Load1 /= n
		avg.DiskReadLat /= n
		avg.DiskWriteLat /= n
		avg.DiskQueue /= n
		avg.Ports = int(float64(avg.Ports)/n + 0.5)
		avg.Procs = int(float64(avg.Procs)/n + 0.5)
		out = append(out, avg)
	}
	return out
}

// Baseline returns the median and p95 of the last minutes of samples.
// It is the recent normal range, not an absolute health target.
func (s *Store) Baseline(minutes int) (model.Baseline, error) {
	if minutes < 5 {
		minutes = 5
	}
	if minutes > 24*60 {
		minutes = 24 * 60
	}
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute).UnixMilli()
	rows, err := s.db.Query(`SELECT cpu, ram, disk_read_latency, disk_write_latency
		FROM samples WHERE ts >= ?`, cutoff)
	if err != nil {
		return model.Baseline{}, err
	}
	defer rows.Close()

	var cpus, rams, readLats, writeLats []float64
	for rows.Next() {
		var cpu, ram, readLat, writeLat float64
		if err := rows.Scan(&cpu, &ram, &readLat, &writeLat); err != nil {
			return model.Baseline{}, err
		}
		cpus = append(cpus, cpu)
		rams = append(rams, ram)
		readLats = append(readLats, readLat)
		writeLats = append(writeLats, writeLat)
	}
	if err := rows.Err(); err != nil {
		return model.Baseline{}, err
	}
	return model.Baseline{
		Samples:        len(cpus),
		Minutes:        minutes,
		CPU50:          percentile(cpus, 50),
		CPU95:          percentile(cpus, 95),
		RAM50:          percentile(rams, 50),
		RAM95:          percentile(rams, 95),
		DiskReadLat95:  percentile(readLats, 95),
		DiskWriteLat95: percentile(writeLats, 95),
	}, nil
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

// InsertAlert stores a new alert and returns its ID.
func (s *Store) InsertAlert(a *model.Alert) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO alerts
		(key, kind, severity, message, value, threshold, started_at, updated_at, ended_at, acked)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0)`,
		a.Key, a.Kind, a.Severity, a.Message, a.Value, a.Threshold, a.StartedAt, a.UpdatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateAlert updates the peak value, severity, message and timestamp of an
// active alert.
func (s *Store) UpdateAlert(a *model.Alert) error {
	_, err := s.db.Exec(`UPDATE alerts SET severity = ?, message = ?, value = ?, threshold = ?, updated_at = ?
		WHERE id = ? AND ended_at = 0`,
		a.Severity, a.Message, a.Value, a.Threshold, a.UpdatedAt, a.ID)
	return err
}

// ResolveAlert closes an alert.
func (s *Store) ResolveAlert(a *model.Alert) error {
	_, err := s.db.Exec(`UPDATE alerts SET severity = ?, message = ?, value = ?, threshold = ?, updated_at = ?, ended_at = ?
		WHERE id = ? AND ended_at = 0`,
		a.Severity, a.Message, a.Value, a.Threshold, a.UpdatedAt, a.EndedAt, a.ID)
	return err
}

// AckAlert marks an alert as acknowledged.
func (s *Store) AckAlert(id int64) error {
	_, err := s.db.Exec(`UPDATE alerts SET acked = 1 WHERE id = ?`, id)
	return err
}

// ListAlerts returns the newest alerts, active and ended.
func (s *Store) ListAlerts(limit int) ([]model.Alert, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, key, kind, severity, message, value, threshold,
		started_at, updated_at, ended_at, acked
		FROM alerts ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	alerts := make([]model.Alert, 0, limit)
	for rows.Next() {
		var a model.Alert
		var acked int
		if err := rows.Scan(&a.ID, &a.Key, &a.Kind, &a.Severity, &a.Message, &a.Value,
			&a.Threshold, &a.StartedAt, &a.UpdatedAt, &a.EndedAt, &acked); err != nil {
			return nil, err
		}
		a.Acked = acked != 0
		alerts = append(alerts, a)
	}
	return alerts, rows.Err()
}

// CloseOpenAlerts ends alerts left open by a previous run, since their
// conditions are no longer sampled.
func (s *Store) CloseOpenAlerts(now int64) error {
	_, err := s.db.Exec(`UPDATE alerts SET ended_at = ?, updated_at = ?
		WHERE ended_at = 0`, now, now)
	return err
}

// Prune removes samples and alerts older than the retention windows. It is
// cheap and safe to call at startup and once an hour.
func (s *Store) Prune(now time.Time) error {
	if _, err := s.db.Exec(`DELETE FROM samples WHERE ts < ?`,
		now.Add(-sampleRetention).UnixMilli()); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM alerts WHERE ended_at > 0 AND ended_at < ?`,
		now.Add(-alertRetention).UnixMilli())
	return err
}
