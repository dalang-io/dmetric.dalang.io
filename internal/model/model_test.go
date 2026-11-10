package model

import (
	"encoding/json"
	"testing"
)

func TestSettingsNormalizeAndValidate(t *testing.T) {
	var settings Settings
	settings.Normalize()
	if settings.Port != DefaultPort || settings.IntervalSeconds != DefaultIntervalSeconds {
		t.Fatalf("normalize did not fill defaults: %+v", settings)
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("default settings are invalid: %v", err)
	}

	bad := settings
	bad.Port = 70000
	if err := bad.Validate(); err == nil {
		t.Fatal("port 70000 was accepted")
	}
	bad = settings
	bad.IntervalSeconds = 0
	if err := bad.Validate(); err == nil {
		t.Fatal("interval 0 was accepted")
	}
	bad = settings
	bad.Thresholds.CPUPercent = 0
	bad.Thresholds.Normalize()
	if bad.Thresholds.CPUPercent != DefaultThresholds().CPUPercent {
		t.Fatalf("zero threshold was not restored: %+v", bad.Thresholds)
	}
	bad = settings
	bad.Thresholds.SustainSeconds = 2
	if err := bad.Validate(); err == nil {
		t.Fatal("sustain of 2s was accepted")
	}
}

func TestFormatters(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{3 << 20, "3.0 MB"},
		{17 << 30, "17.0 GB"},
	}
	for _, c := range cases {
		if got := FormatBytes(c.in); got != c.want {
			t.Errorf("FormatBytes(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := FormatRate(1024); got != "1.0 KB/s" {
		t.Errorf("FormatRate(1024) = %q", got)
	}
}

func TestSettingsWatchAppsAndProbes(t *testing.T) {
	settings := DefaultSettings()
	if len(settings.WatchApps) == 0 {
		t.Fatal("fresh settings should watch at least one app")
	}
	settings.WatchApps = []string{" Chromeleon ", "chromeleon", "", "sqlservr"}
	settings.NetworkProbes = []string{" 127.0.0.1:1433 ", "127.0.0.1:1433", "[::1]:8154"}
	settings.Normalize()
	if len(settings.WatchApps) != 2 || settings.WatchApps[0] != "Chromeleon" {
		t.Fatalf("watch apps were not cleaned: %#v", settings.WatchApps)
	}
	if len(settings.NetworkProbes) != 2 {
		t.Fatalf("probes were not deduplicated: %#v", settings.NetworkProbes)
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("valid probes rejected: %v", err)
	}

	bad := settings
	bad.NetworkProbes = []string{"not-a-host-port"}
	if err := bad.Validate(); err == nil {
		t.Fatal("probe without host:port was accepted")
	}
	bad = settings
	bad.NetworkProbes = []string{"host:99999"}
	if err := bad.Validate(); err == nil {
		t.Fatal("probe with an invalid port was accepted")
	}
	bad = settings
	bad.WatchApps = make([]string, 9)
	if err := bad.Validate(); err == nil {
		t.Fatal("more than 8 watch apps were accepted")
	}
}

func TestSettingsNetworkInterfaces(t *testing.T) {
	settings := DefaultSettings()
	settings.NetworkInterfaces = []string{" en0 ", "EN0", "Ethernet 2", ""}
	settings.Normalize()
	if len(settings.NetworkInterfaces) != 2 || settings.NetworkInterfaces[0] != "en0" {
		t.Fatalf("interfaces not cleaned: %#v", settings.NetworkInterfaces)
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("valid interfaces rejected: %v", err)
	}
	bad := settings
	bad.NetworkInterfaces = make([]string, 65)
	if err := bad.Validate(); err == nil {
		t.Fatal("65 interfaces accepted")
	}
}

func TestUpdateSettingsDefaultsAndValidation(t *testing.T) {
	s := DefaultSettings()
	if s.Update.Mode != UpdateManual || s.Update.IntervalHours != DefaultUpdateIntervalHours {
		t.Fatalf("defaults must be safe: %+v", s.Update)
	}
	// Settings stored before this feature existed have no update block.
	var old Settings
	if err := json.Unmarshal([]byte(`{"port":8154,"interval_seconds":2}`), &old); err != nil {
		t.Fatal(err)
	}
	old.Normalize()
	if old.Update.Mode != UpdateManual || old.Update.IntervalHours != DefaultUpdateIntervalHours {
		t.Fatalf("old settings must normalize to manual/default interval: %+v", old.Update)
	}
	for _, bad := range []UpdateSettings{{Mode: "auto", IntervalHours: 12}, {Mode: UpdateCheck, IntervalHours: -1}, {Mode: UpdateInstall, IntervalHours: 999}} {
		s := DefaultSettings()
		s.Update = bad
		if err := s.Validate(); err == nil {
			t.Errorf("%+v must be rejected", bad)
		}
	}
	for _, mode := range []string{UpdateManual, UpdateCheck, UpdateInstall} {
		s := DefaultSettings()
		s.Update.Mode = mode
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", mode, err)
		}
	}
}
