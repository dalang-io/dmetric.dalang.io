package monitor

import (
	"strings"
	"testing"

	"dmetric.dalang.io/internal/model"
)

func diagSnapshot() model.Snapshot {
	return model.Snapshot{
		CPU:       model.CPUStat{Percent: 10, Cores: 8},
		Memory:    model.MemoryStat{Total: 16 << 30, Used: 6 << 30, Percent: 40},
		Storage:   model.StorageStat{Total: 500 << 30, Used: 100 << 30, Free: 400 << 30, Percent: 20},
		Disk:      model.DiskIOStat{Disks: []model.DiskStat{}},
		Network:   model.NetworkStat{Probes: []model.ProbeResult{}},
		Processes: model.ProcessSummary{Items: []model.ProcessInfo{}},
		Apps:      []model.AppStat{},
		AllProcesses: []model.ProcessInfo{
			{Name: "explorer", CPUPercent: 2, MemoryRSS: 200 << 20},
		},
	}
}

func TestDiagnoseNormalServer(t *testing.T) {
	diag := diagnose(diagSnapshot(), nil, model.DefaultThresholds())
	if diag.Level != "ok" {
		t.Fatalf("level = %q, headline=%q, findings=%+v", diag.Level, diag.Headline, diag.Findings)
	}
	if !strings.Contains(diag.Headline, "tidak menunjukkan") {
		t.Fatalf("unexpected headline: %q", diag.Headline)
	}
}

func TestDiagnoseDiskLatencyWins(t *testing.T) {
	snap := diagSnapshot()
	snap.Storage.Volumes = []model.VolumeStat{{
		Mount: "D:\\", FSType: "NTFS", Total: 2 << 40, Free: 1 << 40,
		HasIO: true, ReadLatencyMs: 4, WriteLatencyMs: 85, Queue: 9,
	}}
	snap.AllProcesses = append(snap.AllProcesses, model.ProcessInfo{
		Name: "sqlservr", DiskWriteBytesPerSec: floatPtr(30e6),
	})
	diag := diagnose(snap, nil, model.DefaultThresholds())
	if diag.Level != "critical" {
		t.Fatalf("level = %q, findings=%+v", diag.Level, diag.Findings)
	}
	if !strings.Contains(diag.Headline, "Disk lambat") || !strings.Contains(diag.Headline, "D:") {
		t.Fatalf("headline = %q", diag.Headline)
	}
	top := diag.Findings[0]
	if !strings.Contains(top.Detail, "sqlservr") {
		t.Fatalf("process attribution missing: %q", top.Detail)
	}
	if !strings.Contains(top.Action, "RAID") {
		t.Fatalf("RAID action missing: %q", top.Action)
	}
}

func TestDiagnoseNetworkLoss(t *testing.T) {
	snap := diagSnapshot()
	snap.Network.Probes = []model.ProbeResult{
		{Target: "10.0.0.20:1433", Kind: "tcp", OK: false, Reachable: false, RTTms: 0, LossPercent: 20, Error: "timeout"},
	}
	diag := diagnose(snap, nil, model.DefaultThresholds())
	if diag.Level != "critical" || !strings.Contains(diag.Headline, "Jaringan") {
		t.Fatalf("level=%q headline=%q findings=%+v", diag.Level, diag.Headline, diag.Findings)
	}
}

func TestDiagnoseRefusedPortIsNotNetworkLoss(t *testing.T) {
	snap := diagSnapshot()
	snap.Network.Probes = []model.ProbeResult{
		{Target: "10.0.0.20:1433", Kind: "tcp", OK: false, Reachable: true, RTTms: 1, LossPercent: 0, Error: "koneksi ditolak"},
	}
	diag := diagnose(snap, nil, model.DefaultThresholds())
	if diag.Level != "ok" {
		t.Fatalf("refused port should not be a network-loss alert: %+v", diag)
	}
	found := false
	for _, finding := range diag.Findings {
		if strings.Contains(finding.Title, "port tertutup") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing refused-port finding: %+v", diag.Findings)
	}
}

func TestDiagnoseSustainedCPU(t *testing.T) {
	snap := diagSnapshot()
	snap.CPU.Percent = 92
	recent := make([]model.HistoryPoint, 30)
	for i := range recent {
		recent[i].CPU = 91
	}
	diag := diagnose(snap, recent, model.DefaultThresholds())
	if diag.Level != "warning" {
		t.Fatalf("level = %q, findings=%+v", diag.Level, diag.Findings)
	}
	found := false
	for _, finding := range diag.Findings {
		if finding.Title == "CPU tinggi (92.0%)" && strings.Contains(finding.Detail, "rata-rata") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing sustained CPU finding: %+v", diag.Findings)
	}
}

func TestDiagnoseHighThroughputWithLowLatencyIsInfo(t *testing.T) {
	snap := diagSnapshot()
	snap.Disk.WriteBytesPerSec = 500e6
	snap.Disk.WriteLatencyMs = 2
	diag := diagnose(snap, nil, model.DefaultThresholds())
	if diag.Level != "ok" {
		t.Fatalf("fast disk should not be a bottleneck: %+v", diag)
	}
	found := false
	for _, finding := range diag.Findings {
		if strings.Contains(finding.Title, "Aktivitas disk tinggi") && finding.Severity == "info" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing informational throughput finding: %+v", diag.Findings)
	}
}

func TestDiagnoseFocusApp(t *testing.T) {
	snap := diagSnapshot()
	snap.Apps = []model.AppStat{{Pattern: "chromeleon", Processes: 3, CPUPercent: 60, MemoryRSS: 5 << 30}}
	diag := diagnose(snap, nil, model.DefaultThresholds())
	found := false
	for _, finding := range diag.Findings {
		if strings.Contains(finding.Title, "Beban aplikasi: chromeleon") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing focus app finding: %+v", diag.Findings)
	}
}

func TestDiagnoseBaselineContext(t *testing.T) {
	snap := diagSnapshot()
	snap.CPU.Percent = 40
	snap.Baseline = model.Baseline{Samples: 200, Minutes: 60, CPU50: 5, CPU95: 10, RAM50: 40, RAM95: 50}
	diag := diagnose(snap, nil, model.DefaultThresholds())
	if diag.Level != "ok" {
		t.Fatalf("baseline context should not change the level: %+v", diag)
	}
	found := false
	for _, finding := range diag.Findings {
		if strings.Contains(finding.Title, "CPU di atas kebiasaan") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing baseline finding: %+v", diag.Findings)
	}
}

func TestDiagnoseQueueUnsupported(t *testing.T) {
	snap := diagSnapshot()
	snap.Disk.Queue = 9
	snap.Disk.QueueSupported = false
	snap.Storage.Volumes = []model.VolumeStat{{
		Mount: "D:\\", HasIO: true, Queue: 9, QueueSupported: false, WriteLatencyMs: 1,
	}}
	diag := diagnose(snap, nil, model.DefaultThresholds())
	for _, finding := range diag.Findings {
		if strings.Contains(finding.Title, "Antrean disk") {
			t.Fatalf("queue should be ignored when unsupported: %+v", finding)
		}
	}

	snap.Storage.Volumes[0].QueueSupported = true
	diag = diagnose(snap, nil, model.DefaultThresholds())
	found := false
	for _, finding := range diag.Findings {
		if strings.Contains(finding.Title, "Antrean disk") {
			found = true
		}
	}
	if !found {
		t.Fatalf("queue finding missing when supported: %+v", diag.Findings)
	}
}
