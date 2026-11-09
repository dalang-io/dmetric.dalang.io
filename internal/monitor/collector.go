package monitor

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"dmetric.dalang.io/internal/diskid"
	"dmetric.dalang.io/internal/model"
)

const (
	processCPUListSize   = 12
	processMemListSize   = 10
	processReadListSize  = 8
	processWriteListSize = 8
	portListLimit        = 200
	volumeListLimit      = 12
	minVolumeBytes       = 1 << 30 // 1 GiB
)

type procCPUState struct {
	cpuSeconds float64
}

type procIOState struct {
	read  uint64
	write uint64
}

// Collector samples the machine. Sample must be called from one goroutine.
type Collector struct {
	hostInfo   model.HostInfo
	first      bool
	ratesReady bool
	prober     *networkProber

	prevTime time.Time
	prevNet  map[string]gnet.IOCountersStat
	prevDisk map[string]disk.IOCountersStat
	prevProc map[int32]procCPUState
	prevIO   map[int32]procIOState

	physicalDisks map[string]bool

	// diskIDs maps kernel names (sda, sde2) to reboot-stable identities. It is
	// rebuilt at most once a minute; udev aliases change rarely.
	diskIDs   map[string]string
	diskIDsAt time.Time
	diskIndex *diskid.Index
}

// New primes the counters so the first sample has rates.
func NewCollector() *Collector {
	c := &Collector{
		first:    true,
		prevNet:  map[string]gnet.IOCountersStat{},
		prevDisk: map[string]disk.IOCountersStat{},
		prevProc: map[int32]procCPUState{},
		prevIO:   map[int32]procIOState{},
	}
	c.hostInfo = c.collectHost()
	c.physicalDisks = listPhysicalDisks()

	// Prime the net and disk counters. CPU is primed by the first sample,
	// which measures a short interval instead of the whole boot time.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if counters, err := gnet.IOCountersWithContext(ctx, true); err == nil {
		for _, n := range counters {
			c.prevNet[n.Name] = n
		}
	}
	if counters, err := disk.IOCountersWithContext(diskIOTimeContext(ctx)); err == nil {
		c.prevDisk = counters
	}
	c.prevTime = time.Now()
	return c
}

func (c *Collector) collectHost() model.HostInfo {
	info := model.HostInfo{
		OS:    runtime.GOOS,
		Arch:  runtime.GOARCH,
		Cores: runtime.NumCPU(),
	}
	if hi, err := host.Info(); err == nil {
		info.Hostname = hi.Hostname
		info.OS = hi.OS
		info.Platform = hi.Platform
		info.PlatformVersion = hi.PlatformVersion
		info.Arch = hi.KernelArch
		info.UptimeSeconds = hi.Uptime
	}
	if info.Hostname == "" {
		info.Hostname, _ = os.Hostname()
	}
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		info.CPUModel = strings.TrimSpace(infos[0].ModelName)
	}
	if info.CPUModel == "" {
		info.CPUModel = runtime.GOARCH
	}
	return info
}

// Sample collects one snapshot of every metric.
func (c *Collector) Sample(ctx context.Context) model.Snapshot {
	now := time.Now()
	elapsed := now.Sub(c.prevTime).Seconds()
	if elapsed <= 0 {
		elapsed = float64(model.DefaultIntervalSeconds)
	}
	c.prevTime = now

	snap := model.Snapshot{
		Ts:           now.UnixMilli(),
		Host:         c.hostInfo,
		Alerts:       []model.Alert{},
		Ports:        model.PortSummary{Items: []model.PortInfo{}},
		Processes:    model.ProcessSummary{Items: []model.ProcessInfo{}},
		AllProcesses: []model.ProcessInfo{},
		Apps:         []model.AppStat{},
	}

	rateElapsed := elapsed
	if !c.ratesReady {
		// The counters were primed a moment ago; a rate over that window
		// would be noise (and could report tens of GB/s).
		rateElapsed = 0
	}
	snap.CPU, snap.Memory, snap.Storage, snap.Disk, snap.Network = c.system(ctx, rateElapsed, &snap.Warnings)

	procs, names := c.processes(ctx, rateElapsed, snap.Memory.Total, &snap.Warnings)
	snap.AllProcesses = procs
	snap.Processes = summarizeProcesses(procs)

	ports, err := c.ports(ctx, names)
	if err != nil {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("port: %v", err))
	}
	snap.Ports = ports
	c.ratesReady = true

	return snap
}

func (c *Collector) system(ctx context.Context, elapsed float64, warnings *[]string) (
	model.CPUStat, model.MemoryStat, model.StorageStat, model.DiskIOStat, model.NetworkStat) {
	var cpuStat model.CPUStat
	var memStat model.MemoryStat
	var storage model.StorageStat
	queueSupported := runtime.GOOS == "linux"
	diskIO := model.DiskIOStat{Disks: []model.DiskStat{}, QueueSupported: queueSupported}
	network := model.NetworkStat{Probes: []model.ProbeResult{}, Interfaces: []model.NetInterfaceStat{}}
	storage = model.StorageStat{Volumes: []model.VolumeStat{}}

	cpuStat.Cores = c.hostInfo.Cores
	interval := time.Duration(0)
	if c.first {
		interval = 250 * time.Millisecond
		c.first = false
	}
	if pcts, err := cpu.PercentWithContext(ctx, interval, false); err == nil && len(pcts) > 0 {
		cpuStat.Percent = clamp(pcts[0], 0, 100)
	} else if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("cpu: %v", err))
	}
	if avg, err := load.AvgWithContext(ctx); err == nil {
		cpuStat.Load1 = clean(avg.Load1)
		cpuStat.Load5 = clean(avg.Load5)
		cpuStat.Load15 = clean(avg.Load15)
		cpuStat.LoadSupported = true
	}

	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		memStat.Total = vm.Total
		memStat.Used = vm.Used
		memStat.Available = vm.Available
		memStat.Free = vm.Free
		memStat.Percent = clamp(vm.UsedPercent, 0, 100)
	} else {
		*warnings = append(*warnings, fmt.Sprintf("memori: %v", err))
	}
	if swap, err := mem.SwapMemoryWithContext(ctx); err == nil {
		memStat.SwapTotal = swap.Total
		memStat.SwapUsed = swap.Used
		memStat.SwapPercent = clamp(swap.UsedPercent, 0, 100)
	}

	mount := "/"
	if runtime.GOOS == "windows" {
		mount = `C:\`
		if drive := os.Getenv("SystemDrive"); drive != "" {
			mount = drive + `\`
		}
	}
	if usage, err := disk.UsageWithContext(ctx, mount); err == nil {
		storage.Total = usage.Total
		storage.Used = usage.Used
		storage.Free = usage.Free
		storage.Percent = clamp(usage.UsedPercent, 0, 100)
		storage.Mount = mount
	} else {
		*warnings = append(*warnings, fmt.Sprintf("penyimpanan: %v", err))
	}

	if counters, err := disk.IOCountersWithContext(diskIOTimeContext(ctx)); err == nil {
		var readTotal, writeTotal uint64
		var readOpsTotal, writeOpsTotal, readTimeTotal, writeTimeTotal float64
		for name, cur := range counters {
			if !c.keepDisk(name) {
				continue
			}
			readTotal += cur.ReadBytes
			writeTotal += cur.WriteBytes
			entry := model.DiskStat{ID: c.diskID(name), Name: name, Queue: float64(cur.IopsInProgress), QueueSupported: queueSupported}
			diskIO.Queue += entry.Queue
			if prev, ok := c.prevDisk[name]; ok && elapsed > 0 {
				entry.ReadBytesPerSec = rate(cur.ReadBytes, prev.ReadBytes, elapsed)
				entry.WriteBytesPerSec = rate(cur.WriteBytes, prev.WriteBytes, elapsed)
				entry.ReadOpsPerSec = rate(cur.ReadCount, prev.ReadCount, elapsed)
				entry.WriteOpsPerSec = rate(cur.WriteCount, prev.WriteCount, elapsed)
				readDelta := counterDelta(cur.ReadTime, prev.ReadTime)
				writeDelta := counterDelta(cur.WriteTime, prev.WriteTime)
				opsRead := counterDelta(cur.ReadCount, prev.ReadCount)
				opsWrite := counterDelta(cur.WriteCount, prev.WriteCount)
				if opsRead > 0 {
					entry.ReadLatencyMs = float64(readDelta) / float64(opsRead)
				}
				if opsWrite > 0 {
					entry.WriteLatencyMs = float64(writeDelta) / float64(opsWrite)
				}
				diskIO.ReadBytesPerSec += entry.ReadBytesPerSec
				diskIO.WriteBytesPerSec += entry.WriteBytesPerSec
				diskIO.ReadOpsPerSec += entry.ReadOpsPerSec
				diskIO.WriteOpsPerSec += entry.WriteOpsPerSec
				readOpsTotal += float64(opsRead)
				writeOpsTotal += float64(opsWrite)
				readTimeTotal += float64(readDelta)
				writeTimeTotal += float64(writeDelta)
			}
			diskIO.Disks = append(diskIO.Disks, entry)
		}
		if readOpsTotal > 0 {
			diskIO.ReadLatencyMs = readTimeTotal / readOpsTotal
		}
		if writeOpsTotal > 0 {
			diskIO.WriteLatencyMs = writeTimeTotal / writeOpsTotal
		}
		diskIO.ReadBytesTotal = readTotal
		diskIO.WriteBytesTotal = writeTotal
		sort.Slice(diskIO.Disks, func(i, j int) bool { return diskIO.Disks[i].Name < diskIO.Disks[j].Name })
		storage.Volumes = c.volumes(ctx, counters, elapsed, warnings)
		if runtime.GOOS == "windows" && len(counters) == 0 {
			*warnings = append(*warnings, "disk io: counter kosong; jalankan sebagai Administrator dan cek 'diskperf -y'")
		}
		c.prevDisk = counters
	} else {
		if runtime.GOOS == "windows" {
			*warnings = append(*warnings, fmt.Sprintf("disk io: %v (jalankan sebagai Administrator; cek diskperf -y)", err))
		} else {
			*warnings = append(*warnings, fmt.Sprintf("disk io: %v", err))
		}
	}

	if counters, err := gnet.IOCountersWithContext(ctx, true); err == nil {
		for _, cur := range counters {
			if isLoopback(cur.Name) {
				continue
			}
			entry := model.NetInterfaceStat{
				Name:         cur.Name,
				RxBytesTotal: cur.BytesRecv,
				TxBytesTotal: cur.BytesSent,
				Selected:     true,
			}
			if prev, ok := c.prevNet[cur.Name]; ok {
				entry.RxBytesPerSec = rate(cur.BytesRecv, prev.BytesRecv, elapsed)
				entry.TxBytesPerSec = rate(cur.BytesSent, prev.BytesSent, elapsed)
			}
			network.Interfaces = append(network.Interfaces, entry)
			network.RxBytesTotal += entry.RxBytesTotal
			network.TxBytesTotal += entry.TxBytesTotal
			network.RxBytesPerSec += entry.RxBytesPerSec
			network.TxBytesPerSec += entry.TxBytesPerSec
		}
		sort.Slice(network.Interfaces, func(i, j int) bool {
			return network.Interfaces[i].Name < network.Interfaces[j].Name
		})
		c.prevNet = map[string]gnet.IOCountersStat{}
		for _, cur := range counters {
			c.prevNet[cur.Name] = cur
		}
	} else {
		*warnings = append(*warnings, fmt.Sprintf("jaringan: %v", err))
	}

	return cpuStat, memStat, storage, diskIO, network
}

func (c *Collector) processes(ctx context.Context, elapsed float64, memTotal uint64, warnings *[]string) ([]model.ProcessInfo, map[int32]string) {
	all, err := process.ProcessesWithContext(ctx)
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("proses: %v", err))
		return nil, map[int32]string{}
	}

	names := make(map[int32]string, len(all))
	infos := make([]model.ProcessInfo, 0, len(all))
	nextCPU := make(map[int32]procCPUState, len(all))
	byPID := make(map[int32]*process.Process, len(all))

	for _, p := range all {
		byPID[p.Pid] = p
		name, _ := p.NameWithContext(ctx)
		name = strings.TrimSpace(name)
		if name == "" {
			name = fmt.Sprintf("pid %d", p.Pid)
		}
		names[p.Pid] = name

		var cpuPct float64
		if ts, err := p.TimesWithContext(ctx); err == nil {
			current := ts.User + ts.System
			nextCPU[p.Pid] = procCPUState{cpuSeconds: current}
			if prev, ok := c.prevProc[p.Pid]; ok {
				cpuPct = clamp((current-prev.cpuSeconds)/elapsed*100, 0, float64(c.hostInfo.Cores)*100)
			}
		}

		var rss uint64
		if mi, err := p.MemoryInfoWithContext(ctx); err == nil && mi != nil {
			rss = mi.RSS
		}
		var memPct float64
		if memTotal > 0 {
			memPct = float64(rss) / float64(memTotal) * 100
		}

		infos = append(infos, model.ProcessInfo{
			PID:           p.Pid,
			Name:          name,
			CPUPercent:    cpuPct,
			MemoryRSS:     rss,
			MemoryPercent: memPct,
		})
	}
	c.prevProc = nextCPU

	// Disk I/O for every process, so a process that hammers the disk but
	// uses little CPU or RAM still shows up. Windows exposes this widely;
	// macOS denies many system processes, which then stay without a rate.
	nextIO := make(map[int32]procIOState, len(all))
	ioRates := make(map[int32][2]float64, len(all))
	for _, p := range all {
		io, err := p.IOCountersWithContext(ctx)
		if err != nil || io == nil {
			continue
		}
		read := io.DiskReadBytes
		write := io.DiskWriteBytes
		if read == 0 && write == 0 {
			read, write = io.ReadBytes, io.WriteBytes
		}
		nextIO[p.Pid] = procIOState{read: read, write: write}
		if prev, ok := c.prevIO[p.Pid]; ok {
			ioRates[p.Pid] = [2]float64{
				rate(read, prev.read, elapsed),
				rate(write, prev.write, elapsed),
			}
		}
	}
	c.prevIO = nextIO
	for i := range infos {
		if rw, ok := ioRates[infos[i].PID]; ok {
			r, w := rw[0], rw[1]
			infos[i].DiskReadBytesPerSec = &r
			infos[i].DiskWriteBytesPerSec = &w
		}
	}

	idx := selectIndexes(infos, processCPUListSize, processMemListSize, processReadListSize, processWriteListSize)

	// Enrich the processes the UI shows: the OS often truncates the short
	// name (macOS keeps 16 bytes).
	for _, i := range idx {
		if p := byPID[infos[i].PID]; p != nil {
			if exe, err := p.ExeWithContext(ctx); err == nil {
				if base := filepath.Base(exe); base != "" && base != "." && len(base) >= len(infos[i].Name) {
					infos[i].Name = base
					names[infos[i].PID] = base
				}
			}
		}
	}
	return infos, names
}

func (c *Collector) ports(ctx context.Context, names map[int32]string) (model.PortSummary, error) {
	conns, err := gnet.ConnectionsWithContext(ctx, "inet")
	if err != nil {
		return model.PortSummary{Items: []model.PortInfo{}}, err
	}

	byKey := map[string]*model.PortInfo{}
	distinct := map[string]bool{}
	for _, conn := range conns {
		proto := ""
		switch conn.Type {
		case 1: // SOCK_STREAM
			if !strings.EqualFold(conn.Status, "LISTEN") {
				continue
			}
			proto = "tcp"
		case 2: // SOCK_DGRAM
			// A connected UDP socket is a client, not a bound service.
			if conn.Raddr.Port != 0 {
				continue
			}
			proto = "udp"
		default:
			continue
		}
		if conn.Laddr.Port == 0 {
			continue
		}
		addr := normalizeAddr(conn.Laddr.IP)
		key := fmt.Sprintf("%s|%d|%d", proto, conn.Laddr.Port, conn.Pid)
		if existing, ok := byKey[key]; ok {
			if addr == "*" {
				existing.Address = "*"
			} else if existing.Address != "*" && !strings.Contains(existing.Address, addr) {
				existing.Address += ", " + addr
			}
			continue
		}
		byKey[key] = &model.PortInfo{
			Proto:   proto,
			Address: addr,
			Port:    conn.Laddr.Port,
			PID:     conn.Pid,
			Process: names[conn.Pid],
		}
		distinct[fmt.Sprintf("%s|%d", proto, conn.Laddr.Port)] = true
	}

	items := make([]model.PortInfo, 0, len(byKey))
	for _, item := range byKey {
		if item.Process == "" {
			item.Process = fmt.Sprintf("pid %d", item.PID)
		}
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Port != items[j].Port {
			return items[i].Port < items[j].Port
		}
		if items[i].Proto != items[j].Proto {
			return items[i].Proto < items[j].Proto
		}
		return items[i].PID < items[j].PID
	})
	if len(items) > portListLimit {
		items = items[:portListLimit]
	}
	return model.PortSummary{Count: len(distinct), Items: items}, nil
}

func summarizeProcesses(all []model.ProcessInfo) model.ProcessSummary {
	idx := selectIndexes(all, processCPUListSize, processMemListSize, processReadListSize, processWriteListSize)
	shown := make([]model.ProcessInfo, 0, len(idx))
	for _, i := range idx {
		shown = append(shown, all[i])
	}
	sort.Slice(shown, func(i, j int) bool {
		if shown[i].CPUPercent != shown[j].CPUPercent {
			return shown[i].CPUPercent > shown[j].CPUPercent
		}
		return shown[i].MemoryRSS > shown[j].MemoryRSS
	})
	return model.ProcessSummary{Count: len(all), Items: shown}
}

// selectIndexes returns the top consumers by CPU, memory and disk read and
// write, without duplicates.
func selectIndexes(all []model.ProcessInfo, byCPU, byMem, byRead, byWrite int) []int {
	seen := map[int32]bool{}
	out := make([]int, 0, byCPU+byMem+byRead+byWrite)
	add := func(indexes []int) {
		for _, i := range indexes {
			if seen[all[i].PID] {
				continue
			}
			seen[all[i].PID] = true
			out = append(out, i)
		}
	}
	add(topIndexes(all, byCPU, func(p model.ProcessInfo) float64 { return p.CPUPercent }))
	add(topIndexes(all, byMem, func(p model.ProcessInfo) float64 { return float64(p.MemoryRSS) }))
	add(topIndexes(all, byRead, func(p model.ProcessInfo) float64 {
		if p.DiskReadBytesPerSec == nil {
			return 0
		}
		return *p.DiskReadBytesPerSec
	}))
	add(topIndexes(all, byWrite, func(p model.ProcessInfo) float64 {
		if p.DiskWriteBytesPerSec == nil {
			return 0
		}
		return *p.DiskWriteBytesPerSec
	}))
	return out
}

func topIndexes(all []model.ProcessInfo, limit int, score func(model.ProcessInfo) float64) []int {
	idx := make([]int, 0, len(all))
	for i := range all {
		if score(all[i]) > 0 {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(i, j int) bool {
		return score(all[idx[i]]) > score(all[idx[j]])
	})
	if len(idx) > limit {
		idx = idx[:limit]
	}
	return idx
}

func normalizeAddr(ip string) string {
	switch ip {
	case "", "*", "0.0.0.0", "::", "::0":
		return "*"
	}
	return ip
}

func isLoopback(name string) bool {
	n := strings.ToLower(name)
	return n == "lo" || n == "lo0" || strings.Contains(n, "loopback")
}

func rate(current, previous uint64, elapsed float64) float64 {
	if current < previous || elapsed <= 0 {
		return 0
	}
	return float64(current-previous) / elapsed
}

var realFilesystems = map[string]bool{
	"ntfs": true, "refs": true, "fat": true, "fat32": true, "exfat": true,
	"apfs": true, "hfs": true, "ext2": true, "ext3": true, "ext4": true,
	"xfs": true, "btrfs": true, "zfs": true, "f2fs": true, "jfs": true,
	"reiserfs": true, "ufs": true, "vmfs": true,
}

func (c *Collector) volumes(ctx context.Context, counters map[string]disk.IOCountersStat, elapsed float64, warnings *[]string) []model.VolumeStat {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("volume: %v", err))
		return []model.VolumeStat{}
	}
	out := make([]model.VolumeStat, 0, len(parts))
	seenMount := make(map[string]bool, len(parts))
	seenDevice := make(map[string]bool, len(parts))
	seenSize := make(map[string]bool, len(parts))
	for _, part := range parts {
		mount := strings.TrimSpace(part.Mountpoint)
		if mount == "" || seenMount[mount] || !relevantVolumeMount(mount) {
			continue
		}
		fstype := strings.ToLower(part.Fstype)
		if fstype != "" && !realFilesystems[fstype] {
			continue
		}
		deviceKey := strings.TrimSpace(part.Device)
		if deviceKey != "" && seenDevice[deviceKey] {
			continue
		}
		if runtime.GOOS == "linux" && virtualVolumeDevice(deviceKey) {
			// Ceph RBD / loop images are not physical storage; on a
			// hypervisor they would bury the real volumes.
			continue
		}
		usage, err := disk.UsageWithContext(ctx, mount)
		if err != nil || usage == nil || usage.Total < minVolumeBytes {
			continue
		}
		if runtime.GOOS == "darwin" {
			// APFS volumes in one container share capacity; keep the first.
			sizeKey := fmt.Sprintf("%d|%d", usage.Total, usage.Free)
			if seenSize[sizeKey] {
				continue
			}
			seenSize[sizeKey] = true
		}
		seenMount[mount] = true
		if deviceKey != "" {
			seenDevice[deviceKey] = true
		}
		volume := model.VolumeStat{
			Mount:          mount,
			Device:         part.Device,
			FSType:         part.Fstype,
			Total:          usage.Total,
			Used:           usage.Used,
			Free:           usage.Free,
			Percent:        clamp(usage.UsedPercent, 0, 100),
			QueueSupported: runtime.GOOS == "linux",
		}
		if runtime.GOOS == "linux" {
			for _, key := range volumeIOKeys(part) {
				if id := c.diskID(key); id != "" {
					volume.DiskID = id
					break
				}
			}
		}
		for _, key := range volumeIOKeys(part) {
			cur, ok := counters[key]
			if !ok {
				continue
			}
			volume.HasIO = true
			volume.IOKey = key
			volume.Queue = float64(cur.IopsInProgress)
			if prev, ok := c.prevDisk[key]; ok && elapsed > 0 {
				volume.ReadBytesPerSec = rate(cur.ReadBytes, prev.ReadBytes, elapsed)
				volume.WriteBytesPerSec = rate(cur.WriteBytes, prev.WriteBytes, elapsed)
				opsRead := counterDelta(cur.ReadCount, prev.ReadCount)
				opsWrite := counterDelta(cur.WriteCount, prev.WriteCount)
				if opsRead > 0 {
					volume.ReadLatencyMs = float64(counterDelta(cur.ReadTime, prev.ReadTime)) / float64(opsRead)
				}
				if opsWrite > 0 {
					volume.WriteLatencyMs = float64(counterDelta(cur.WriteTime, prev.WriteTime)) / float64(opsWrite)
				}
			}
			break
		}
		out = append(out, volume)
		if len(out) >= volumeListLimit {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}

// virtualVolumeDevice reports devices backed by a network or pseudo block
// device (rbd*, loop*), resolving /dev/mapper and by-id symlinks first.
func virtualVolumeDevice(device string) bool {
	if device == "" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(device); err == nil {
		device = resolved
	}
	return diskid.Virtual(filepath.Base(device))
}

// relevantVolumeMount hides the synthetic volumes macOS mounts under
// /System/Volumes, which duplicate the root filesystem or belong to the OS.
func relevantVolumeMount(mount string) bool {
	if runtime.GOOS == "darwin" {
		if strings.HasPrefix(mount, "/System/Volumes/") || strings.HasPrefix(mount, "/private/") {
			return false
		}
	}
	return true
}

func volumeIOKeys(part disk.PartitionStat) []string {
	if runtime.GOOS == "windows" {
		mount := strings.TrimSpace(part.Mountpoint)
		if len(mount) >= 2 && mount[1] == ':' {
			return []string{mount[:2]}
		}
	}
	device := strings.TrimSpace(part.Device)
	if device != "" && runtime.GOOS != "windows" {
		if resolved, err := filepath.EvalSymlinks(device); err == nil {
			device = resolved
		}
	}
	base := filepath.Base(device)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return nil
	}
	keys := []string{base}
	if whole := wholeDiskName(base); whole != "" && whole != base {
		keys = append(keys, whole)
	}
	return keys
}

// wholeDiskName turns sda1, nvme0n1p1 and mmcblk0p1 into their parent disk
// name. Stacked devices (dm-0, md0) are their own counters and return "".
func wholeDiskName(name string) string { return diskid.PartitionParent(name) }

// diskID returns the stable identity for a kernel device name (disk or
// partition), or "" when none is known (non-Linux, or no udev alias).
func (c *Collector) diskID(kernel string) string {
	if runtime.GOOS != "linux" || kernel == "" {
		return ""
	}
	if c.diskIndex == nil || time.Since(c.diskIDsAt) > time.Minute {
		c.diskIDsAt = time.Now()
		c.diskIndex = diskid.Load(diskid.DefaultByIDDir)
		names := []string{}
		if entries, err := os.ReadDir(diskid.DefaultSysBlock); err == nil {
			for _, e := range entries {
				if diskid.Physical(e.Name()) {
					names = append(names, e.Name())
				}
			}
		}
		c.diskIDs = c.diskIndex.Resolve(diskid.DefaultSysBlock, names)
	}
	if id, ok := c.diskIDs[kernel]; ok {
		return id
	}
	return c.diskIndex.ID(kernel)
}

func counterDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Min(math.Max(v, lo), hi)
}

func clean(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

// diskIOTimeContext asks gopsutil for millisecond read/write times on
// Windows. Without it Windows reports seconds, while Linux and macOS
// report milliseconds, and latency would be off by 1000x.
func diskIOTimeContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, disk.WillBeDeletedOptInWindowsIOTimeMs, true)
}

// keepDisk filters out partitions and pseudo devices on Linux so the disk
// counters are not counted twice; macOS and Windows report whole drives.
func (c *Collector) keepDisk(name string) bool {
	if c.physicalDisks == nil {
		return true
	}
	if len(c.physicalDisks) == 0 {
		return true
	}
	return c.physicalDisks[name]
}

func listPhysicalDisks() map[string]bool {
	if runtime.GOOS != "linux" {
		return nil
	}
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return physicalDiskSet(names)
}

// physicalDiskSet chooses the block devices whose counters are summed.
// Pseudo devices are dropped, and stacked devices (LVM/LUKS, RAID, bcache,
// DRBD, network block) are dropped when a physical parent exists, since
// their I/O is already counted there and summing both would double it.
// When only stacked devices are exposed, they are used instead.
func physicalDiskSet(names []string) map[string]bool {
	all := make(map[string]bool, len(names))
	physical := make(map[string]bool, len(names))
	for _, name := range names {
		if pseudoDisk(name) {
			continue
		}
		all[name] = true
		if !stackedDisk(name) {
			physical[name] = true
		}
	}
	if len(physical) > 0 {
		return physical
	}
	return all
}

// pseudoDisk drops loop/ram/optical devices and network block devices (Ceph
// RBD) so a hypervisor with dozens of RBD images is not summed as local disk.
func pseudoDisk(name string) bool { return diskid.Virtual(name) }

func stackedDisk(name string) bool { return diskid.Stacked(name) }

// HostInfo returns the cached machine description.
func (c *Collector) HostInfo() model.HostInfo { return c.hostInfo }
