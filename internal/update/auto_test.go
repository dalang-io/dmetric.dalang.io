package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dmetric.dalang.io/internal/model"
)

// releaseServer serves a fake newer GitHub release. withChecksum controls
// whether SHA256SUMS is published.
func releaseServer(t *testing.T, withChecksum bool, hits *int32) *httptest.Server {
	t.Helper()
	content := []byte("new binary")
	sum := sha256.Sum256(content)
	mux := http.NewServeMux()
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) { w.Write(content) })
	mux.HandleFunc("/sha", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + assetName() + "\n"))
	})
	mux.HandleFunc("/release", func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		assets := []map[string]any{{"name": assetName(), "browser_download_url": "http://" + r.Host + "/asset", "size": len(content)}}
		if withChecksum {
			assets = append(assets, map[string]any{"name": "SHA256SUMS", "browser_download_url": "http://" + r.Host + "/sha", "size": 80})
		}
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v2026.12.0", "html_url": "https://example.test", "assets": assets})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// newTestManager points a Manager at the fake release and replaces the
// installer, so no test can overwrite a real binary.
func newTestManager(t *testing.T, server *httptest.Server) (*Manager, *int32) {
	t.Helper()
	m := New(t.TempDir(), "2026.11.0", "test/repo")
	m.apiURL = server.URL + "/release"
	m.canInstall = func() error { return nil }
	installs := new(int32)
	m.installFn = func(string) (func() error, error) {
		atomic.AddInt32(installs, 1)
		return func() error { return nil }, nil
	}
	return m, installs
}

func TestAutoCheckModeNeverInstalls(t *testing.T) {
	m, installs := newTestManager(t, releaseServer(t, true, nil))
	called := false
	m.autoOnce(context.Background(), Policy{Mode: model.UpdateCheck}, func(Result) { called = true })
	if called || atomic.LoadInt32(installs) != 0 {
		t.Fatal("check mode must never install")
	}
	st := m.AutoStatus()
	if !strings.Contains(st.LastResult, "update tersedia") || st.LastCheck == "" {
		t.Fatalf("status: %+v", st)
	}
}

func TestAutoInstallRequiresChecksum(t *testing.T) {
	m, installs := newTestManager(t, releaseServer(t, false, nil)) // no SHA256SUMS
	m.autoOnce(context.Background(), Policy{Mode: model.UpdateInstall}, func(Result) { t.Fatal("must not install without a checksum") })
	if atomic.LoadInt32(installs) != 0 {
		t.Fatal("installed a release without a verifiable checksum")
	}
	if st := m.AutoStatus(); !strings.Contains(st.LastResult, "SHA256") {
		t.Fatalf("status should explain the refusal: %q", st.LastResult)
	}
}

func TestAutoInstallSuccessThenNoRetryLoop(t *testing.T) {
	m, installs := newTestManager(t, releaseServer(t, true, nil))
	var restarted int32
	onInstalled := func(Result) { atomic.AddInt32(&restarted, 1) }

	m.autoOnce(context.Background(), Policy{Mode: model.UpdateInstall}, onInstalled)
	if atomic.LoadInt32(installs) != 1 || atomic.LoadInt32(&restarted) != 1 {
		t.Fatalf("installs=%d restarts=%d, want 1/1", *installs, restarted)
	}
	if st := m.AutoStatus(); st.LastInstall == "" || !strings.Contains(st.LastResult, "terpasang") {
		t.Fatalf("status: %+v", st)
	}
	// The same release again within 24h (e.g. the install did not stick and
	// the process restarted) must not download and restart in a loop.
	m.autoOnce(context.Background(), Policy{Mode: model.UpdateInstall}, onInstalled)
	if atomic.LoadInt32(installs) != 1 || atomic.LoadInt32(&restarted) != 1 {
		t.Fatalf("retried a recently attempted version: installs=%d restarts=%d", *installs, restarted)
	}
	if st := m.AutoStatus(); !strings.Contains(st.LastResult, "tidak diulang") {
		t.Fatalf("status: %q", st.LastResult)
	}
}

func TestAutoRetryAllowedAfterWindow(t *testing.T) {
	m, _ := newTestManager(t, releaseServer(t, true, nil))
	m.markAttempt("2026.12.0")
	if !m.recentlyAttempted("2026.12.0") || m.recentlyAttempted("2027.1.0") {
		t.Fatal("marker must apply to that version only")
	}
	old, _ := json.Marshal(attemptMarker{Version: "2026.12.0", At: time.Now().Add(-25 * time.Hour).Format(time.RFC3339)})
	if err := os.WriteFile(m.attemptPath(), old, 0o644); err != nil {
		t.Fatal(err)
	}
	if m.recentlyAttempted("2026.12.0") {
		t.Fatal("an attempt older than 24h must allow a retry")
	}
}

func TestAutoInstallBlockedFallsBackToNotify(t *testing.T) {
	m, installs := newTestManager(t, releaseServer(t, true, nil))
	m.canInstall = func() error { return errors.New("aplikasi macOS dibundel (.app)") }
	m.autoOnce(context.Background(), Policy{Mode: model.UpdateInstall}, func(Result) { t.Fatal("must not install") })
	if atomic.LoadInt32(installs) != 0 {
		t.Fatal("installed although self-install is blocked")
	}
	st := m.AutoStatus()
	if st.CanInstall || !strings.Contains(st.LastResult, "tidak didukung") {
		t.Fatalf("status: %+v", st)
	}
}

func TestRunAutoManualNeverContactsGitHub(t *testing.T) {
	var hits int32
	m, _ := newTestManager(t, releaseServer(t, true, &hits))
	m.startupDelay, m.autoPoll = time.Millisecond, 2*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	m.RunAuto(ctx, func() Policy { return Policy{Mode: model.UpdateManual, Interval: time.Millisecond} }, nil)
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("manual mode contacted the release API %d times", n)
	}
}

func TestRunAutoChecksAtStartupThenEveryInterval(t *testing.T) {
	var hits int32
	m, _ := newTestManager(t, releaseServer(t, true, &hits))
	m.startupDelay, m.autoPoll = 5*time.Millisecond, 2*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 260*time.Millisecond)
	defer cancel()
	m.RunAuto(ctx, func() Policy { return Policy{Mode: model.UpdateCheck, Interval: 100 * time.Millisecond} }, nil)
	n := atomic.LoadInt32(&hits)
	if n < 2 || n > 4 {
		t.Fatalf("expected a startup check plus ~1-2 interval checks, got %d", n)
	}
}

func TestRunAutoPicksUpPolicyChanges(t *testing.T) {
	var hits int32
	m, _ := newTestManager(t, releaseServer(t, true, &hits))
	m.startupDelay, m.autoPoll = 5*time.Millisecond, 2*time.Millisecond
	var mode atomic.Value
	mode.Store(model.UpdateManual)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go func() {
		time.Sleep(60 * time.Millisecond)
		mode.Store(model.UpdateCheck) // operator enables checks in Settings
	}()
	m.RunAuto(ctx, func() Policy { return Policy{Mode: mode.Load().(string), Interval: time.Hour} }, nil)
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("enabling the mode should trigger one prompt check, got %d", hits)
	}
}
