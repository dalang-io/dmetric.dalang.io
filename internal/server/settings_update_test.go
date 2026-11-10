package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"dmetric.dalang.io/internal/model"
	"dmetric.dalang.io/internal/monitor"
	"dmetric.dalang.io/internal/store"
)

func newSettingsTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dmetric.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	settings := model.DefaultSettings()
	mon := monitor.New(st, st, time.Second, settings.Thresholds, settings.WatchApps, nil, nil)
	return New(mon, st, settings, settings.Port, settings.IntervalSeconds, "127.0.0.1", t.TempDir(), false, false)
}

func putSettings(s *Server, remote string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(raw))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	s.handlePutSettings(rec, req)
	return rec
}

func settingsBody(update *model.UpdateSettings) map[string]any {
	d := model.DefaultSettings()
	body := map[string]any{
		"port": d.Port, "interval_seconds": d.IntervalSeconds, "thresholds": d.Thresholds,
		"watch_apps": d.WatchApps, "network_probes": d.NetworkProbes,
	}
	if update != nil {
		body["update"] = update
	}
	return body
}

func TestUpdatePolicyOnlyChangeableFromLocalhost(t *testing.T) {
	s := newSettingsTestServer(t)
	on := &model.UpdateSettings{Mode: model.UpdateInstall, IntervalHours: 24}

	if rec := putSettings(s, "10.0.0.50:51234", settingsBody(on)); rec.Code != http.StatusForbidden {
		t.Fatalf("LAN client enabling auto-install got %d, want 403", rec.Code)
	}
	if s.UpdatePolicy().Mode != model.UpdateManual {
		t.Fatal("policy changed despite the 403")
	}
	if rec := putSettings(s, "127.0.0.1:51234", settingsBody(on)); rec.Code != http.StatusOK {
		t.Fatalf("localhost change got %d: %s", rec.Code, rec.Body.String())
	}
	p := s.UpdatePolicy()
	if p.Mode != model.UpdateInstall || p.Interval != 24*time.Hour {
		t.Fatalf("policy = %+v", p)
	}
}

func TestLANClientCanStillSaveOtherSettings(t *testing.T) {
	s := newSettingsTestServer(t)
	body := settingsBody(&model.UpdateSettings{Mode: model.UpdateManual, IntervalHours: 24}) // unchanged
	th := model.DefaultThresholds()
	th.CPUPercent = 70
	body["thresholds"] = th
	if rec := putSettings(s, "10.0.0.50:51234", body); rec.Code != http.StatusOK {
		t.Fatalf("saving thresholds from the LAN got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestOlderClientDoesNotResetUpdatePolicy(t *testing.T) {
	s := newSettingsTestServer(t)
	on := &model.UpdateSettings{Mode: model.UpdateCheck, IntervalHours: 6}
	if rec := putSettings(s, "127.0.0.1:1", settingsBody(on)); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	// A client that does not know about the update block omits it entirely.
	if rec := putSettings(s, "10.0.0.50:2", settingsBody(nil)); rec.Code != http.StatusOK {
		t.Fatalf("omitting update block got %d: %s", rec.Code, rec.Body.String())
	}
	if p := s.UpdatePolicy(); p.Mode != model.UpdateCheck || p.Interval != 6*time.Hour {
		t.Fatalf("policy was reset by a client that omitted it: %+v", p)
	}
}

func TestInvalidUpdatePolicyRejected(t *testing.T) {
	s := newSettingsTestServer(t)
	bad := &model.UpdateSettings{Mode: "yolo", IntervalHours: 24}
	if rec := putSettings(s, "127.0.0.1:1", settingsBody(bad)); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode got %d, want 400", rec.Code)
	}
}
