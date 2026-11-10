package update

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"dmetric.dalang.io/internal/model"
)

// retryWindow is how long a failed or attempted auto-install of one version
// is not repeated, so a bad release cannot cause a download/restart loop.
const retryWindow = 24 * time.Hour

// Policy is the automatic update behaviour at one moment.
type Policy struct {
	Mode     string // model.UpdateManual | UpdateCheck | UpdateInstall
	Interval time.Duration
}

// AutoStatus is what the dashboard shows about the automatic updater.
type AutoStatus struct {
	LastCheck   string `json:"last_check,omitempty"`   // RFC3339
	LastResult  string `json:"last_result,omitempty"`  // human readable
	LastInstall string `json:"last_install,omitempty"` // RFC3339
	// CanInstall is false when this process cannot replace itself (macOS
	// .app, `go run`); the mode then only checks.
	CanInstall  bool   `json:"can_install"`
	BlockReason string `json:"block_reason,omitempty"`
}

// AutoStatus returns the latest automatic-update state.
func (m *Manager) AutoStatus() AutoStatus {
	m.autoMu.Lock()
	status := m.auto
	m.autoMu.Unlock()
	if err := m.canInstall(); err != nil {
		status.CanInstall, status.BlockReason = false, err.Error()
	} else {
		status.CanInstall, status.BlockReason = true, ""
	}
	return status
}

func (m *Manager) setAuto(update func(*AutoStatus)) {
	m.autoMu.Lock()
	update(&m.auto)
	m.autoMu.Unlock()
}

// RunAuto checks for updates in the background until ctx ends: once shortly
// after startup, then every Policy.Interval. Policy is re-read every poll so
// a settings change applies without a restart. In install mode it downloads,
// verifies and installs the release and calls onInstalled (which restarts).
// In manual mode it never contacts GitHub.
func (m *Manager) RunAuto(ctx context.Context, policy func() Policy, onInstalled func(Result)) {
	next := time.Now().Add(m.startupDelay)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.autoPoll):
		}
		p := policy()
		if p.Mode != model.UpdateCheck && p.Mode != model.UpdateInstall {
			// Switching on later checks soon, not a full interval later.
			next = time.Now().Add(m.startupDelay)
			continue
		}
		if time.Now().Before(next) {
			continue
		}
		m.autoOnce(ctx, p, onInstalled)
		interval := p.Interval
		if interval <= 0 {
			interval = 24 * time.Hour
		}
		next = time.Now().Add(interval)
	}
}

// autoOnce performs one automatic check (and install when allowed).
func (m *Manager) autoOnce(ctx context.Context, p Policy, onInstalled func(Result)) {
	info := m.Check(ctx, true)
	now := time.Now()
	record := func(result string) {
		m.setAuto(func(a *AutoStatus) {
			a.LastCheck = now.Format(time.RFC3339)
			a.LastResult = result
		})
		log.Printf("dmetric: update otomatis: %s", result)
	}

	switch {
	case info.Error != "":
		record("gagal memeriksa rilis: " + info.Error)
		return
	case !info.UpdateAvailable:
		record("sudah memakai versi terbaru (" + info.Tag + ")")
		return
	case p.Mode != model.UpdateInstall:
		record("update tersedia: " + info.Tag + " (mode cek otomatis; pasang dari dashboard)")
		return
	}

	if err := m.canInstall(); err != nil {
		record(fmt.Sprintf("update tersedia: %s; pemasangan otomatis tidak didukung: %v", info.Tag, err))
		return
	}
	if m.recentlyAttempted(info.LatestVersion) {
		record(fmt.Sprintf("update %s sudah dicoba dalam 24 jam terakhir; tidak diulang otomatis", info.Tag))
		return
	}
	// Mark before installing: a crash or restart loop must not retry.
	m.markAttempt(info.LatestVersion)
	result, err := m.InstallWith(ctx, info.LatestVersion, InstallOptions{RequireChecksum: true})
	if err != nil {
		record(fmt.Sprintf("gagal memasang %s: %v", info.Tag, err))
		return
	}
	m.setAuto(func(a *AutoStatus) { a.LastInstall = now.Format(time.RFC3339) })
	record(fmt.Sprintf("%s terpasang (%s); restart", info.Tag, result.Message))
	if onInstalled != nil {
		onInstalled(result)
	}
}

type attemptMarker struct {
	Version string `json:"version"`
	At      string `json:"at"`
}

func (m *Manager) attemptPath() string {
	return filepath.Join(m.dataDir, "updates", "auto-attempt.json")
}

func (m *Manager) recentlyAttempted(version string) bool {
	data, err := os.ReadFile(m.attemptPath())
	if err != nil {
		return false
	}
	var marker attemptMarker
	if json.Unmarshal(data, &marker) != nil || marker.Version != version {
		return false
	}
	at, err := time.Parse(time.RFC3339, marker.At)
	return err == nil && time.Since(at) < retryWindow
}

func (m *Manager) markAttempt(version string) {
	if err := os.MkdirAll(filepath.Dir(m.attemptPath()), 0o755); err != nil {
		return
	}
	data, _ := json.Marshal(attemptMarker{Version: version, At: time.Now().Format(time.RFC3339)})
	_ = os.WriteFile(m.attemptPath(), data, 0o644)
}
