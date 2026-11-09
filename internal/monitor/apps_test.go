package monitor

import (
	"testing"

	"dmetric.dalang.io/internal/model"
)

func floatPtr(v float64) *float64 { return &v }

func TestAppStats(t *testing.T) {
	procs := []model.ProcessInfo{
		{Name: "Chromeleon", CPUPercent: 20, MemoryRSS: 1 << 30, DiskReadBytesPerSec: floatPtr(2e6), DiskWriteBytesPerSec: floatPtr(1e6)},
		{Name: "ChromeleonInstrumentServer", CPUPercent: 30, MemoryRSS: 2 << 30, DiskWriteBytesPerSec: floatPtr(3e6)},
		{Name: "sqlservr", CPUPercent: 10, MemoryRSS: 4 << 30},
		{Name: "explorer", CPUPercent: 90, MemoryRSS: 1 << 30},
	}
	stats := appStats(procs, []string{"chromeleon", "sqlservr", "missing"})
	if len(stats) != 3 {
		t.Fatalf("want 3 groups, got %#v", stats)
	}
	chromeleon := stats[0]
	if chromeleon.Processes != 2 || chromeleon.CPUPercent != 50 {
		t.Fatalf("unexpected Chromeleon group: %#v", chromeleon)
	}
	if chromeleon.MemoryRSS != 3<<30 {
		t.Fatalf("memory not summed: %d", chromeleon.MemoryRSS)
	}
	if chromeleon.DiskReadBytesPerSec != 2e6 || chromeleon.DiskWriteBytesPerSec != 4e6 {
		t.Fatalf("disk I/O not summed: %#v", chromeleon)
	}
	if chromeleon.TopProcess != "ChromeleonInstrumentServer" {
		t.Fatalf("top process = %q", chromeleon.TopProcess)
	}
	if stats[1].Processes != 1 || stats[1].TopProcess != "sqlservr" {
		t.Fatalf("unexpected SQL Server group: %#v", stats[1])
	}
	if stats[2].Processes != 0 {
		t.Fatalf("missing pattern should be an empty group: %#v", stats[2])
	}
}

func TestSanitizePatterns(t *testing.T) {
	got := sanitizePatterns([]string{" Chromeleon ", "chromeleon", "", "sqlservr"})
	if len(got) != 2 || got[0] != "Chromeleon" || got[1] != "sqlservr" {
		t.Fatalf("sanitizePatterns = %#v", got)
	}
}
