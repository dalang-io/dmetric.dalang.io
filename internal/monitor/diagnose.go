package monitor

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"dmetric.dalang.io/internal/model"
)

const (
	diskLatencyWarn     = 20.0 // ms
	diskLatencyCritical = 50.0
	diskQueueWarn       = 2.0
	diskQueueCritical   = 8.0
	probeRTTWarn        = 100.0 // ms
	probeRTTCritical    = 300.0
	probeLossWarn       = 1.0 // percent
	probeLossCritical   = 5.0
)

// diagnose ranks the most likely causes of slowness from the current
// sample and the last few minutes. It is deliberately explicit about what
// is and is not evidenced; a normal server is a valid diagnosis.
func diagnose(s model.Snapshot, recent []model.HistoryPoint, th model.Thresholds) model.Diagnosis {
	findings := make([]model.Finding, 0, 8)
	add := func(severity, title, detail, action string) {
		findings = append(findings, model.Finding{
			Severity: severity,
			Title:    title,
			Detail:   detail,
			Action:   action,
		})
	}

	procs := s.AllProcesses
	if len(procs) == 0 {
		procs = s.Processes.Items
	}
	cpuTop := topProcesses(procs, 3,
		func(p model.ProcessInfo) float64 { return p.CPUPercent },
		func(p model.ProcessInfo) string { return fmt.Sprintf("%s %.0f%%", p.Name, p.CPUPercent) })
	memTop := topProcesses(procs, 3,
		func(p model.ProcessInfo) float64 { return float64(p.MemoryRSS) },
		func(p model.ProcessInfo) string {
			return fmt.Sprintf("%s %s", p.Name, model.FormatBytes(float64(p.MemoryRSS)))
		})
	ioTop := topProcesses(procs, 3,
		func(p model.ProcessInfo) float64 { return processIO(p) },
		func(p model.ProcessInfo) string { return fmt.Sprintf("%s %s", p.Name, model.FormatRate(processIO(p))) })

	// Hardware health outranks resource saturation: a failing disk, ECC
	// error or degraded array is a cause, not a symptom.
	if s.Hardware.Status == "critical" || s.Hardware.Status == "warning" {
		detail := strings.Join(limitIssues(s.Hardware.Issues, 4), "; ")
		if detail == "" {
			detail = "Pemeriksaan hardware melaporkan masalah."
		}
		add(s.Hardware.Status, "Kesehatan hardware: "+fmt.Sprintf("%d temuan", len(s.Hardware.Issues)), detail,
			"Backup data, cek SMART/RAID (iLO/SSA), dan ganti komponen yang ditandai sebelum menyalahkan aplikasi.")
	}

	// Disk latency is the best single signal that storage is the
	// bottleneck, especially on a RAID 5 of spinning disks where a queue
	// can build long before throughput peaks.
	worstVol, hasVol := worstVolume(s.Storage.Volumes)
	readLat, writeLat, queue, diskName := s.Disk.ReadLatencyMs, s.Disk.WriteLatencyMs, s.Disk.Queue, ""
	queueSupported := s.Disk.QueueSupported
	if hasVol {
		readLat, writeLat, queue = worstVol.ReadLatencyMs, worstVol.WriteLatencyMs, worstVol.Queue
		queueSupported = worstVol.QueueSupported
		diskName = worstVol.Mount
	} else if worstDisk, ok := worstDiskLatency(s.Disk.Disks); ok {
		readLat, writeLat, queue = worstDisk.ReadLatencyMs, worstDisk.WriteLatencyMs, worstDisk.Queue
		queueSupported = worstDisk.QueueSupported
		diskName = worstDisk.Name
		if worstDisk.ID != "" {
			// Kernel names (sdb) reshuffle across reboots; show the stable id.
			diskName = worstDisk.ID
		}
	}
	if !queueSupported {
		queue = 0
	}
	latency := math.Max(readLat, writeLat)
	hadLatencyWarning := false
	if latency >= diskLatencyWarn {
		hadLatencyWarning = true
		severity := "warning"
		if latency >= diskLatencyCritical {
			severity = "critical"
		}
		target := diskName
		if target == "" {
			target = "disk"
		}
		detail := fmt.Sprintf("%s: latensi baca %.1f ms, tulis %.1f ms; %s; baca %.1f MB/s, tulis %.1f MB/s.",
			target, readLat, writeLat, queueText(queue, queueSupported), s.Disk.ReadBytesPerSec/1e6, s.Disk.WriteBytesPerSec/1e6)
		if hasVol {
			detail += fmt.Sprintf(" Volume %s (%s), bebas %s dari %s.",
				worstVol.Mount, worstVol.FSType, model.FormatBytes(float64(worstVol.Free)), model.FormatBytes(float64(worstVol.Total)))
		}
		if baselineReady(s.Baseline) {
			detail += fmt.Sprintf(" p95 1 jam: baca %.1f ms, tulis %.1f ms.", s.Baseline.DiskReadLat95, s.Baseline.DiskWriteLat95)
		}
		if ioTop != "" {
			detail += " Proses I/O tertinggi: " + ioTop + "."
		}
		add(severity, fmt.Sprintf("Disk lambat: %s (%.1f ms)", target, latency), detail,
			"Cek baterai/cache RAID di iLO/SSA, status rebuild/degraded, antivirus real-time, dan antrean SQL Server. RAID 5 HDD memang lambat untuk tulis acak.")
	}
	if queueSupported && queue >= diskQueueWarn {
		severity := "warning"
		if queue >= diskQueueCritical {
			severity = "critical"
		}
		add(severity, fmt.Sprintf("Antrean disk tinggi (%.1f)", queue),
			fmt.Sprintf("Antrean menunjukkan permintaan menumpuk; latensi baca %.1f ms, tulis %.1f ms. Proses I/O tertinggi: %s.", readLat, writeLat, ioTop),
			"Kurangi I/O bersamaan (backup, antivirus, reporting Chromeleon), cek cache RAID dan antrean disk di Windows.")
	}

	// CPU and load.
	avgCPU, above, samples := recentStats(recent, func(p model.HistoryPoint) float64 { return p.CPU }, th.CPUPercent)
	if s.CPU.Percent >= th.CPUPercent {
		severity := "warning"
		if s.CPU.Percent >= th.CPUPercent+10 || s.CPU.Percent >= 95 {
			severity = "critical"
		}
		detail := fmt.Sprintf("Sekarang %.1f%%, rata-rata %.1f%% (%d dari %d sampel terakhir di atas %.0f%%). Proses CPU tertinggi: %s.",
			s.CPU.Percent, avgCPU, above, samples, th.CPUPercent, cpuTop)
		if baselineReady(s.Baseline) {
			detail += fmt.Sprintf(" p95 1 jam %.1f%%.", s.Baseline.CPU95)
		}
		add(severity, fmt.Sprintf("CPU tinggi (%.1f%%)", s.CPU.Percent), detail,
			"Cek proses CPU tertinggi; jadwalkan reporting/processing Chromeleon dan query SQL Server yang berjalan.")
	}
	if s.CPU.LoadSupported && s.CPU.Cores > 0 {
		perCore := s.CPU.Load1 / float64(s.CPU.Cores)
		if perCore >= th.LoadPerCore {
			add("warning", fmt.Sprintf("Load tinggi (%.2f per core)", perCore),
				fmt.Sprintf("Load 1m %.2f pada %d core, rata-rata CPU %.1f%%. Proses CPU tertinggi: %s.", s.CPU.Load1, s.CPU.Cores, avgCPU, cpuTop),
				"Periksa proses yang antre (CPU, disk, atau thread SQL); load tinggi tanpa CPU tinggi biasanya berarti menunggu disk.")
		}
	}

	// Memory.
	if s.Memory.Percent >= th.MemoryPercent {
		severity := "warning"
		if s.Memory.Percent >= 95 {
			severity = "critical"
		}
		detail := fmt.Sprintf("%s / %s terpakai (%.1f%%), swap %s. Proses RAM tertinggi: %s.",
			model.FormatBytes(float64(s.Memory.Used)), model.FormatBytes(float64(s.Memory.Total)),
			s.Memory.Percent, model.FormatBytes(float64(s.Memory.SwapUsed)), memTop)
		if baselineReady(s.Baseline) {
			detail += fmt.Sprintf(" p95 1 jam %.1f%%.", s.Baseline.RAM95)
		}
		add(severity, fmt.Sprintf("RAM tinggi (%.1f%%)", s.Memory.Percent), detail,
			"Batasi max memory SQL Server agar menyisakan RAM untuk OS, cek memori Chromeleon, dan pagefile.")
	}

	// Full volumes, including a data volume that is not the system volume.
	if vol, ok := fullestVolume(s.Storage.Volumes); ok {
		if vol.Percent >= th.StoragePercent {
			add("warning", fmt.Sprintf("Volume %s hampir penuh (%.0f%%)", vol.Mount, vol.Percent),
				fmt.Sprintf("Bebas %s dari %s. Volume %s (%s).", model.FormatBytes(float64(vol.Free)), model.FormatBytes(float64(vol.Total)), vol.Mount, vol.FSType),
				"Bersihkan log/temp, cek pertumbuhan database Chromeleon dan file backup.")
		}
	} else if s.Storage.Percent >= th.StoragePercent {
		add("warning", fmt.Sprintf("Penyimpanan hampir penuh (%.0f%%)", s.Storage.Percent),
			fmt.Sprintf("Bebas %s dari %s.", model.FormatBytes(float64(s.Storage.Free)), model.FormatBytes(float64(s.Storage.Total))),
			"Bersihkan log/temp dan cek pertumbuhan database Chromeleon.")
	}

	// Throughput, even when latency is not available.
	readMB := s.Disk.ReadBytesPerSec / 1e6
	writeMB := s.Disk.WriteBytesPerSec / 1e6
	if readMB >= th.DiskReadMBps || writeMB >= th.DiskWriteMBps {
		busy := latency >= diskLatencyWarn || (queueSupported && queue >= diskQueueWarn)
		severity := "info"
		action := "Latensi masih normal, jadi throughput tinggi saja belum tentu penyebab lemot. Lihat proses I/O tertinggi."
		if busy {
			severity = "warning"
			action = "I/O tinggi disertai latensi/antrean tinggi: kurangi I/O bersamaan, cek proses I/O, RAID, dan antivirus."
		}
		add(severity, fmt.Sprintf("Aktivitas disk tinggi (%.0f MB/s baca, %.0f MB/s tulis)", readMB, writeMB),
			fmt.Sprintf("Latensi baca %.1f ms, tulis %.1f ms, %s. Proses I/O tertinggi: %s.",
				readLat, writeLat, queueText(queue, queueSupported), ioTop),
			action)
	}

	// Network: TCP probes are authoritative; an unanswered ICMP gateway is
	// often just a firewall and is reported separately.
	probes := s.Network.Probes
	tcpProbes := make([]model.ProbeResult, 0, len(probes))
	for _, probe := range probes {
		if probe.Kind == "tcp" {
			tcpProbes = append(tcpProbes, probe)
		}
	}
	if len(tcpProbes) > 0 {
		worstRTT := 0.0
		worstLoss := 0.0
		worstTarget := ""
		for _, probe := range tcpProbes {
			if probe.RTTms > worstRTT {
				worstRTT = probe.RTTms
				worstTarget = probe.Target
			}
			if probe.LossPercent > worstLoss {
				worstLoss = probe.LossPercent
			}
		}
		refused := make([]string, 0, len(tcpProbes))
		for _, probe := range tcpProbes {
			if !probe.OK && probe.Reachable {
				refused = append(refused, probe.Target)
			}
		}
		if len(refused) > 0 {
			add("info", "Target menerima koneksi tetapi port tertutup",
				"Host merespons (jaringan dasar sehat), tetapi service menolak koneksi: "+strings.Join(refused, ", ")+".",
				"Pastikan SQL Server/Chromeleon berjalan dan port benar, atau ganti target probe.")
		}
		if worstLoss >= probeLossWarn || worstRTT >= probeRTTWarn {
			severity := "warning"
			if worstLoss >= probeLossCritical || worstRTT >= probeRTTCritical {
				severity = "critical"
			}
			detail := fmt.Sprintf("Probe terburuk %s: %.1f ms, loss %.1f%%. Semua probe: %s.", worstTarget, worstRTT, worstLoss, probeSummary(tcpProbes))
			add(severity, "Jaringan lambat/tidak stabil", detail,
				"Uji dari PC client yang lemot, cek switch/NIC teaming/VPN, dan probe ke IP SQL serta IP client Chromeleon.")
		}
	} else {
		add("info", "Belum ada probe jaringan",
			"Latensi ke SQL Server atau PC client belum diukur; gateway hanya diuji dengan ICMP.",
			"Tambahkan host:port SQL/Chromeleon di Pengaturan, lalu buka dashboard dari PC client untuk melihat latensi browser→server.")
	}

	// Baseline context: a value can be normal for this server or unusual
	// for it even below the fixed alert threshold. These are informational,
	// not suspects, so a changed workload does not raise an alarm.
	if baselineReady(s.Baseline) {
		if s.CPU.Percent >= 25 && s.CPU.Percent > s.Baseline.CPU95*1.5 {
			add("info", fmt.Sprintf("CPU di atas kebiasaan (%.1f%% vs p95 %.1f%%)", s.CPU.Percent, s.Baseline.CPU95),
				fmt.Sprintf("Median 1 jam %.1f%%. Proses CPU tertinggi: %s.", s.Baseline.CPU50, cpuTop),
				"Jika tidak ada pekerjaan baru Chromeleon/SQL, cari proses yang tidak biasa; jika ada batch/reporting, ini wajar.")
		}
		if s.Memory.Percent >= 70 && s.Memory.Percent > s.Baseline.RAM95*1.15 {
			add("info", fmt.Sprintf("RAM di atas kebiasaan (%.1f%% vs p95 %.1f%%)", s.Memory.Percent, s.Baseline.RAM95),
				fmt.Sprintf("Median 1 jam %.1f%%. Proses RAM tertinggi: %s.", s.Baseline.RAM50, memTop),
				"Cek pertumbuhan memori proses atau pagefile; jika baru, bandingkan dengan jadwal aplikasi.")
		}
		writeBaseline := math.Max(readLat, writeLat)
		baselineLat := math.Max(s.Baseline.DiskReadLat95, s.Baseline.DiskWriteLat95)
		if !hadLatencyWarning && writeBaseline >= 5 && writeBaseline > math.Max(5, baselineLat*2) {
			add("info", fmt.Sprintf("Latensi disk di atas kebiasaan (%.1f ms vs p95 %.1f ms)", writeBaseline, baselineLat),
				"Belum melewati ambang absolut, tetapi lebih tinggi dari kebiasaan 1 jam. Proses I/O tertinggi: "+ioTop+".",
				"Pantau tren latensi; jika naik terus, cek RAID/antivirus/backup sebelum menjadi bottleneck.")
		}
	}

	// Focus applications: attribute load to Chromeleon/SQL Server as a whole.
	for _, app := range s.Apps {
		if app.Processes == 0 {
			continue
		}
		ioRate := app.DiskReadBytesPerSec + app.DiskWriteBytesPerSec
		if app.CPUPercent >= 50 || app.MemoryRSS >= 8<<30 || ioRate >= 10e6 {
			severity := "info"
			if app.CPUPercent >= 80 || app.MemoryRSS >= 16<<30 || ioRate >= 50e6 {
				severity = "warning"
			}
			detail := fmt.Sprintf("%d proses, CPU %.1f%%, RAM %s, disk %s. Proses utama: %s.",
				app.Processes, app.CPUPercent, model.FormatBytes(float64(app.MemoryRSS)), model.FormatRate(ioRate), app.TopProcess)
			add(severity, "Beban aplikasi: "+app.Pattern, detail,
				"Bandingkan angka ini saat normal vs saat lemot; cek queue/pekerjaan aplikasi dan query SQL yang berjalan.")
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		return severityRank(findings[i].Severity) < severityRank(findings[j].Severity)
	})

	level := "ok"
	headline := "Resource server tidak menunjukkan bottleneck yang jelas"
	hasWarn := false
	for _, finding := range findings {
		if finding.Severity == "warning" || finding.Severity == "critical" {
			hasWarn = true
			break
		}
	}
	if hasWarn {
		level = findings[0].Severity
		headline = "Tersangka utama: " + findings[0].Title
	} else {
		add("info", "Resource server dalam batas normal",
			fmt.Sprintf("CPU %.1f%%, RAM %.1f%%, latensi disk %.1f ms, %s. Tidak ada saturasi resource yang menjelaskan lemot.",
				s.CPU.Percent, s.Memory.Percent, latency, queueText(queue, queueSupported)),
			"Buka dashboard dari PC client yang lemot, aktifkan probe ke IP SQL/client, catat insiden saat lemot, lalu periksa SQL Server wait stats dan log/audit Chromeleon.")
	}

	if len(findings) > 6 {
		findings = findings[:6]
	}
	return model.Diagnosis{Level: level, Headline: headline, Findings: findings}
}

func limitIssues(issues []string, limit int) []string {
	if len(issues) <= limit {
		return issues
	}
	out := append([]string(nil), issues[:limit]...)
	out = append(out, fmt.Sprintf("… %d masalah lain", len(issues)-limit))
	return out
}

func baselineReady(b model.Baseline) bool {
	return b.Samples >= 120
}

func queueText(queue float64, supported bool) string {
	if !supported {
		return "antrean — (tidak tersedia di OS ini)"
	}
	return fmt.Sprintf("antrean %.1f", queue)
}

func severityRank(severity string) int {
	switch severity {
	case "critical":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}

func processIO(p model.ProcessInfo) float64 {
	var total float64
	if p.DiskReadBytesPerSec != nil {
		total += *p.DiskReadBytesPerSec
	}
	if p.DiskWriteBytesPerSec != nil {
		total += *p.DiskWriteBytesPerSec
	}
	return total
}

func topProcesses(procs []model.ProcessInfo, limit int, score func(model.ProcessInfo) float64, render func(model.ProcessInfo) string) string {
	indexes := make([]int, 0, len(procs))
	for i := range procs {
		if score(procs[i]) > 0 {
			indexes = append(indexes, i)
		}
	}
	sort.SliceStable(indexes, func(i, j int) bool {
		return score(procs[indexes[i]]) > score(procs[indexes[j]])
	})
	if len(indexes) > limit {
		indexes = indexes[:limit]
	}
	parts := make([]string, 0, len(indexes))
	for _, i := range indexes {
		parts = append(parts, render(procs[i]))
	}
	if len(parts) == 0 {
		return "tidak terbaca"
	}
	return strings.Join(parts, ", ")
}

func recentStats(points []model.HistoryPoint, value func(model.HistoryPoint) float64, threshold float64) (avg float64, above, total int) {
	if len(points) == 0 {
		return 0, 0, 0
	}
	sum := 0.0
	for _, point := range points {
		v := value(point)
		sum += v
		if v >= threshold {
			above++
		}
	}
	return sum / float64(len(points)), above, len(points)
}

func worstVolume(volumes []model.VolumeStat) (model.VolumeStat, bool) {
	best := model.VolumeStat{}
	bestLatency := 0.0
	for _, volume := range volumes {
		if !volume.HasIO {
			continue
		}
		latency := math.Max(volume.ReadLatencyMs, volume.WriteLatencyMs)
		if latency > bestLatency {
			bestLatency = latency
			best = volume
		}
	}
	return best, bestLatency > 0
}

func worstDiskLatency(disks []model.DiskStat) (model.DiskStat, bool) {
	best := model.DiskStat{}
	bestLatency := 0.0
	for _, disk := range disks {
		latency := math.Max(disk.ReadLatencyMs, disk.WriteLatencyMs)
		if latency > bestLatency {
			bestLatency = latency
			best = disk
		}
	}
	return best, bestLatency > 0
}

func fullestVolume(volumes []model.VolumeStat) (model.VolumeStat, bool) {
	best := model.VolumeStat{}
	for _, volume := range volumes {
		if volume.Percent > best.Percent {
			best = volume
		}
	}
	return best, best.Mount != ""
}

func probeSummary(probes []model.ProbeResult) string {
	parts := make([]string, 0, len(probes))
	for _, probe := range probes {
		status := "OK"
		if !probe.OK {
			status = probe.Error
			if status == "" {
				status = "gagal"
			}
		}
		parts = append(parts, fmt.Sprintf("%s %.1f ms loss %.0f%% (%s)", probe.Target, probe.RTTms, probe.LossPercent, status))
	}
	return strings.Join(parts, "; ")
}
