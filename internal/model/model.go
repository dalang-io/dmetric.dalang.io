// Package model holds the data exchanged by the collector, the store, the
// alert engine, the HTTP API and the web UI.
package model

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Version is the application version, also reported by /api/health. It is a
// variable so release builds set it from the CalVer tag with -X.
var Version = "2026.11.2"

// DefaultPort is the HTTP port of the web app and of the desktop app.
const DefaultPort = 8154

// DefaultIntervalSeconds is the sampling interval of the monitor.
const DefaultIntervalSeconds = 2

// Thresholds are the alert limits. Rates are in megabytes per second and
// memory sizes in megabytes.
type Thresholds struct {
	CPUPercent        float64 `json:"cpu_percent"`
	MemoryPercent     float64 `json:"memory_percent"`
	StoragePercent    float64 `json:"storage_percent"`
	DiskReadMBps      float64 `json:"disk_read_mbps"`
	DiskWriteMBps     float64 `json:"disk_write_mbps"`
	NetRxMBps         float64 `json:"net_rx_mbps"`
	NetTxMBps         float64 `json:"net_tx_mbps"`
	LoadPerCore       float64 `json:"load_per_core"`
	ProcessCPUPercent float64 `json:"process_cpu_percent"`
	ProcessMemoryMB   float64 `json:"process_memory_mb"`
	SustainSeconds    int     `json:"sustain_seconds"`
	RecoverySeconds   int     `json:"recovery_seconds"`
}

// DefaultThresholds returns sensible limits for a desktop or server.
func DefaultThresholds() Thresholds {
	return Thresholds{
		CPUPercent:        85,
		MemoryPercent:     85,
		StoragePercent:    90,
		DiskReadMBps:      100,
		DiskWriteMBps:     100,
		NetRxMBps:         25,
		NetTxMBps:         10,
		LoadPerCore:       1.5,
		ProcessCPUPercent: 80,
		ProcessMemoryMB:   4096,
		SustainSeconds:    15,
		RecoverySeconds:   20,
	}
}

// Normalize fills zero or negative fields with their defaults, so settings
// written by an older version keep working.
func (t *Thresholds) Normalize() {
	d := DefaultThresholds()
	if t.CPUPercent <= 0 {
		t.CPUPercent = d.CPUPercent
	}
	if t.MemoryPercent <= 0 {
		t.MemoryPercent = d.MemoryPercent
	}
	if t.StoragePercent <= 0 {
		t.StoragePercent = d.StoragePercent
	}
	if t.DiskReadMBps <= 0 {
		t.DiskReadMBps = d.DiskReadMBps
	}
	if t.DiskWriteMBps <= 0 {
		t.DiskWriteMBps = d.DiskWriteMBps
	}
	if t.NetRxMBps <= 0 {
		t.NetRxMBps = d.NetRxMBps
	}
	if t.NetTxMBps <= 0 {
		t.NetTxMBps = d.NetTxMBps
	}
	if t.LoadPerCore <= 0 {
		t.LoadPerCore = d.LoadPerCore
	}
	if t.ProcessCPUPercent <= 0 {
		t.ProcessCPUPercent = d.ProcessCPUPercent
	}
	if t.ProcessMemoryMB <= 0 {
		t.ProcessMemoryMB = d.ProcessMemoryMB
	}
	if t.SustainSeconds <= 0 {
		t.SustainSeconds = d.SustainSeconds
	}
	if t.RecoverySeconds <= 0 {
		t.RecoverySeconds = d.RecoverySeconds
	}
}

// Validate checks the ranges the settings API accepts.
func (t Thresholds) Validate() error {
	switch {
	case t.CPUPercent < 1 || t.CPUPercent > 100:
		return errors.New("cpu_percent harus antara 1 dan 100")
	case t.MemoryPercent < 1 || t.MemoryPercent > 100:
		return errors.New("memory_percent harus antara 1 dan 100")
	case t.StoragePercent < 1 || t.StoragePercent > 100:
		return errors.New("storage_percent harus antara 1 dan 100")
	case t.DiskReadMBps < 1 || t.DiskReadMBps > 100000:
		return errors.New("disk_read_mbps harus antara 1 dan 100000")
	case t.DiskWriteMBps < 1 || t.DiskWriteMBps > 100000:
		return errors.New("disk_write_mbps harus antara 1 dan 100000")
	case t.NetRxMBps < 1 || t.NetRxMBps > 100000:
		return errors.New("net_rx_mbps harus antara 1 dan 100000")
	case t.NetTxMBps < 1 || t.NetTxMBps > 100000:
		return errors.New("net_tx_mbps harus antara 1 dan 100000")
	case t.LoadPerCore < 0.1 || t.LoadPerCore > 100:
		return errors.New("load_per_core harus antara 0.1 dan 100")
	case t.ProcessCPUPercent < 1 || t.ProcessCPUPercent > 10000:
		return errors.New("process_cpu_percent harus antara 1 dan 10000")
	case t.ProcessMemoryMB < 1 || t.ProcessMemoryMB > 1000000:
		return errors.New("process_memory_mb harus antara 1 dan 1000000")
	case t.SustainSeconds < 5 || t.SustainSeconds > 600:
		return errors.New("sustain_seconds harus antara 5 dan 600")
	case t.RecoverySeconds < 5 || t.RecoverySeconds > 600:
		return errors.New("recovery_seconds harus antara 5 dan 600")
	}
	return nil
}

// Settings is what the app persists between runs. Port, interval and
// network probes take effect at the next start; thresholds and watch
// patterns apply immediately.
type Settings struct {
	Port            int        `json:"port"`
	IntervalSeconds int        `json:"interval_seconds"`
	Thresholds      Thresholds `json:"thresholds"`
	// WatchApps are case-insensitive name fragments grouped in the focus
	// applications card, e.g. "chromeleon", "sqlservr".
	WatchApps []string `json:"watch_apps"`
	// NetworkProbes are host:port targets whose TCP handshake latency is
	// measured, e.g. "10.0.0.20:1433".
	NetworkProbes []string `json:"network_probes"`
	// NetworkInterfaces selects which non-loopback interfaces are summed
	// into the Internet card. Empty means all of them.
	NetworkInterfaces []string `json:"network_interfaces"`
	// Update controls how DMetric checks for and installs new releases.
	Update UpdateSettings `json:"update"`
}

// Update modes.
const (
	// UpdateManual never contacts GitHub on its own; the operator clicks
	// "Cek" / "Update" (the dashboard may still show a hint when opened).
	UpdateManual = "manual"
	// UpdateCheck looks for a new release at startup and every interval and
	// shows it in the dashboard, but installs nothing.
	UpdateCheck = "check"
	// UpdateInstall also downloads, verifies (SHA256 required) and installs
	// the new release, then restarts.
	UpdateInstall = "install"

	DefaultUpdateIntervalHours = 24
	MaxUpdateIntervalHours     = 168
)

// UpdateSettings is the automatic update policy.
type UpdateSettings struct {
	Mode          string `json:"mode"`
	IntervalHours int    `json:"interval_hours"`
}

// DefaultSettings returns the settings of a fresh installation.
func DefaultSettings() Settings {
	return Settings{
		Port:            DefaultPort,
		IntervalSeconds: DefaultIntervalSeconds,
		Thresholds:      DefaultThresholds(),
		WatchApps:       []string{"chromeleon", "sqlservr"},
		NetworkProbes:   []string{},
		Update:          UpdateSettings{Mode: UpdateManual, IntervalHours: DefaultUpdateIntervalHours},
	}
}

// Normalize fills missing values with their defaults.
func (s *Settings) Normalize() {
	if s.Port == 0 {
		s.Port = DefaultPort
	}
	if s.IntervalSeconds == 0 {
		s.IntervalSeconds = DefaultIntervalSeconds
	}
	s.Thresholds.Normalize()
	if s.Update.Mode == "" {
		s.Update.Mode = UpdateManual
	}
	if s.Update.IntervalHours == 0 {
		s.Update.IntervalHours = DefaultUpdateIntervalHours
	}
	s.WatchApps = cleanList(s.WatchApps)
	s.NetworkProbes = cleanList(s.NetworkProbes)
	s.NetworkInterfaces = cleanList(s.NetworkInterfaces)
}

// cleanList trims, drops empty entries and removes case-insensitive
// duplicates while keeping the first spelling.
func cleanList(list []string) []string {
	if list == nil {
		return nil
	}
	out := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

// Validate checks the settings the API accepts.
func (s Settings) Validate() error {
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("port harus antara 1 dan 65535")
	}
	if s.IntervalSeconds < 1 || s.IntervalSeconds > 60 {
		return errors.New("interval_seconds harus antara 1 dan 60")
	}
	if err := s.Thresholds.Validate(); err != nil {
		return err
	}
	switch s.Update.Mode {
	case UpdateManual, UpdateCheck, UpdateInstall:
	default:
		return errors.New("update.mode harus manual, check, atau install")
	}
	if s.Update.IntervalHours < 1 || s.Update.IntervalHours > MaxUpdateIntervalHours {
		return fmt.Errorf("update.interval_hours harus antara 1 dan %d", MaxUpdateIntervalHours)
	}
	if len(s.WatchApps) > 8 {
		return errors.New("watch_apps maksimal 8 pola")
	}
	for _, app := range s.WatchApps {
		if len(app) > 64 || strings.ContainsAny(app, "\r\n\t") {
			return errors.New("setiap pola watch_apps harus 1-64 karakter tanpa kontrol")
		}
	}
	if len(s.NetworkProbes) > 8 {
		return errors.New("network_probes maksimal 8 target")
	}
	if len(s.NetworkInterfaces) > 64 {
		return errors.New("network_interfaces maksimal 64 interface")
	}
	for _, name := range s.NetworkInterfaces {
		if len(name) > 64 || strings.ContainsAny(name, "\r\n\t") {
			return errors.New("nama interface jaringan tidak valid")
		}
	}
	for _, target := range s.NetworkProbes {
		host, port, err := net.SplitHostPort(target)
		if err != nil || host == "" || len(target) > 255 {
			return fmt.Errorf("target probe %q harus host:port, misalnya 10.0.0.20:1433", target)
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("port pada target probe %q tidak valid", target)
		}
	}
	return nil
}

// HostInfo describes the machine the monitor runs on.
type HostInfo struct {
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	Platform        string `json:"platform"`
	PlatformVersion string `json:"platform_version"`
	Arch            string `json:"arch"`
	CPUModel        string `json:"cpu_model"`
	Cores           int    `json:"cores"`
	UptimeSeconds   uint64 `json:"uptime_seconds"`
}

// CPUStat is the CPU usage of the machine.
type CPUStat struct {
	Percent       float64 `json:"percent"`
	Cores         int     `json:"cores"`
	Load1         float64 `json:"load1"`
	Load5         float64 `json:"load5"`
	Load15        float64 `json:"load15"`
	LoadSupported bool    `json:"load_supported"`
}

// MemoryStat is the RAM usage of the machine.
type MemoryStat struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Available   uint64  `json:"available"`
	Free        uint64  `json:"free"`
	Percent     float64 `json:"percent"`
	SwapTotal   uint64  `json:"swap_total"`
	SwapUsed    uint64  `json:"swap_used"`
	SwapPercent float64 `json:"swap_percent"`
}

// VolumeStat is one mounted filesystem with its usage and, where the OS
// maps it to a disk counter, its I/O latency and queue. On a server with an
// OS RAID 1 SSD and a data RAID 5 HDD this tells the two arrays apart.
type VolumeStat struct {
	Mount            string  `json:"mount"`
	Device           string  `json:"device"`
	FSType           string  `json:"fstype"`
	Total            uint64  `json:"total"`
	Used             uint64  `json:"used"`
	Free             uint64  `json:"free"`
	Percent          float64 `json:"percent"`
	ReadBytesPerSec  float64 `json:"read_bytes_per_sec"`
	WriteBytesPerSec float64 `json:"write_bytes_per_sec"`
	ReadLatencyMs    float64 `json:"read_latency_ms"`
	WriteLatencyMs   float64 `json:"write_latency_ms"`
	Queue            float64 `json:"queue"`
	QueueSupported   bool    `json:"queue_supported"`
	HasIO            bool    `json:"has_io"`
	IOKey            string  `json:"io_key,omitempty"`
	// DiskID is the stable identity of the backing disk/partition so the UI
	// does not show sda/sdb names that change after a reboot.
	DiskID string `json:"disk_id,omitempty"`
}

// StorageStat is the usage of the root volume and every mounted volume.
type StorageStat struct {
	Total   uint64       `json:"total"`
	Used    uint64       `json:"used"`
	Free    uint64       `json:"free"`
	Percent float64      `json:"percent"`
	Mount   string       `json:"mount"`
	Volumes []VolumeStat `json:"volumes"`
}

// DiskStat is one physical disk's throughput, latency and queue.
type DiskStat struct {
	ID               string  `json:"id,omitempty"`
	Name             string  `json:"name"`
	ReadBytesPerSec  float64 `json:"read_bytes_per_sec"`
	WriteBytesPerSec float64 `json:"write_bytes_per_sec"`
	ReadOpsPerSec    float64 `json:"read_ops_per_sec"`
	WriteOpsPerSec   float64 `json:"write_ops_per_sec"`
	ReadLatencyMs    float64 `json:"read_latency_ms"`
	WriteLatencyMs   float64 `json:"write_latency_ms"`
	Queue            float64 `json:"queue"`
	QueueSupported   bool    `json:"queue_supported"`
}

// DiskIOStat is the disk throughput and latency of the machine. Latency and
// queue are what tell a slow disk apart from a busy one.
type DiskIOStat struct {
	ReadBytesPerSec  float64    `json:"read_bytes_per_sec"`
	WriteBytesPerSec float64    `json:"write_bytes_per_sec"`
	ReadBytesTotal   uint64     `json:"read_bytes_total"`
	WriteBytesTotal  uint64     `json:"write_bytes_total"`
	ReadOpsPerSec    float64    `json:"read_ops_per_sec"`
	WriteOpsPerSec   float64    `json:"write_ops_per_sec"`
	ReadLatencyMs    float64    `json:"read_latency_ms"`
	WriteLatencyMs   float64    `json:"write_latency_ms"`
	Queue            float64    `json:"queue"`
	QueueSupported   bool       `json:"queue_supported"`
	Disks            []DiskStat `json:"disks"`
}

// ProbeResult is one gateway ping or TCP handshake probe.
type ProbeResult struct {
	Target      string  `json:"target"`
	Kind        string  `json:"kind"`
	RTTms       float64 `json:"rtt_ms"`
	OK          bool    `json:"ok"`
	Reachable   bool    `json:"reachable"`
	LossPercent float64 `json:"loss_percent"`
	Attempts    int     `json:"attempts"`
	Error       string  `json:"error,omitempty"`
}

// NetInterfaceStat is one non-loopback interface.
type NetInterfaceStat struct {
	Name          string  `json:"name"`
	RxBytesPerSec float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64 `json:"tx_bytes_per_sec"`
	RxBytesTotal  uint64  `json:"rx_bytes_total"`
	TxBytesTotal  uint64  `json:"tx_bytes_total"`
	Selected      bool    `json:"selected"`
}

// NetworkStat is the network throughput and latency of the machine.
type NetworkStat struct {
	RxBytesPerSec float64            `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64            `json:"tx_bytes_per_sec"`
	RxBytesTotal  uint64             `json:"rx_bytes_total"`
	TxBytesTotal  uint64             `json:"tx_bytes_total"`
	Gateway       string             `json:"gateway"`
	Probes        []ProbeResult      `json:"probes"`
	Interfaces    []NetInterfaceStat `json:"interfaces"`
}

// PortInfo is one listening TCP or bound UDP socket.
type PortInfo struct {
	Proto   string `json:"proto"`
	Address string `json:"address"`
	Port    uint32 `json:"port"`
	PID     int32  `json:"pid"`
	Process string `json:"process"`
}

// PortSummary is the port list and the number of distinct ports.
type PortSummary struct {
	Count int        `json:"count"`
	Items []PortInfo `json:"items"`
}

// ProcessInfo is one running process.
type ProcessInfo struct {
	PID           int32   `json:"pid"`
	Name          string  `json:"name"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryRSS     uint64  `json:"memory_rss"`
	MemoryPercent float64 `json:"memory_percent"`
	// The disk rates are nil where the OS does not expose them for the
	// process, which is common on macOS without root.
	DiskReadBytesPerSec  *float64 `json:"disk_read_bytes_per_sec"`
	DiskWriteBytesPerSec *float64 `json:"disk_write_bytes_per_sec"`
}

// ProcessSummary is the process count and the top consumers.
type ProcessSummary struct {
	Count int           `json:"count"`
	Items []ProcessInfo `json:"items"`
}

// AppStat sums the processes of one watched application.
type AppStat struct {
	Pattern              string  `json:"pattern"`
	Processes            int     `json:"processes"`
	CPUPercent           float64 `json:"cpu_percent"`
	MemoryRSS            uint64  `json:"memory_rss"`
	DiskReadBytesPerSec  float64 `json:"disk_read_bytes_per_sec"`
	DiskWriteBytesPerSec float64 `json:"disk_write_bytes_per_sec"`
	TopProcess           string  `json:"top_process"`
}

// Finding is one diagnostic hypothesis with the next thing to check.
type Finding struct {
	Severity string `json:"severity"` // warning, critical, info
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Action   string `json:"action"`
}

// Diagnosis ranks what most likely makes the machine slow.
type Diagnosis struct {
	Level    string    `json:"level"` // ok, warning, critical
	Headline string    `json:"headline"`
	Findings []Finding `json:"findings"`
}

// Baseline is the recent normal range, computed from the samples table.
type Baseline struct {
	Samples        int     `json:"samples"`
	Minutes        int     `json:"minutes"`
	CPU50          float64 `json:"cpu_p50"`
	CPU95          float64 `json:"cpu_p95"`
	RAM50          float64 `json:"ram_p50"`
	RAM95          float64 `json:"ram_p95"`
	DiskReadLat95  float64 `json:"disk_read_latency_p95"`
	DiskWriteLat95 float64 `json:"disk_write_latency_p95"`
}

// Hardware is the machine's component inventory and health, refreshed much
// less often than the resource samples.
type Hardware struct {
	Source       string          `json:"source"`
	CollectedAt  int64           `json:"collected_at"`
	Status       string          `json:"status"` // ok, warning, critical, unknown
	System       HWSystem        `json:"system"`
	CPU          HWCPU           `json:"cpu"`
	Memory       HWMemory        `json:"memory"`
	Accelerators []HWAccelerator `json:"accelerators"`
	Fans         []HWFan         `json:"fans"`
	PSUs         []HWPSU         `json:"psus"`
	Storage      []HWStorage     `json:"storage"`
	Issues       []string        `json:"issues"`
	Notes        []string        `json:"notes,omitempty"`
}

// HWSystem is the motherboard and BIOS inventory.
type HWSystem struct {
	Vendor        string   `json:"vendor"`
	Product       string   `json:"product"`
	Serial        string   `json:"serial"`
	BIOSVersion   string   `json:"bios_version"`
	BIOSDate      string   `json:"bios_date"`
	WHEAErrors    int      `json:"whea_errors"`
	WHEASupported bool     `json:"whea_supported"`
	Status        string   `json:"status"`
	Issues        []string `json:"issues,omitempty"`
}

// HWCPU is the processor inventory and temperature, where exposed.
type HWCPU struct {
	Model             string   `json:"model"`
	Serial            string   `json:"serial"`
	Cores             int      `json:"cores"`
	Threads           int      `json:"threads"`
	MaxMHz            int      `json:"max_mhz"`
	TemperatureC      *float64 `json:"temperature_c"`
	TemperatureSource string   `json:"temperature_source,omitempty"`
	Status            string   `json:"status"`
	Issues            []string `json:"issues,omitempty"`
}

// HWMemory is RAM capacity, modules and ECC counters.
type HWMemory struct {
	Total            uint64     `json:"total"`
	Modules          []HWModule `json:"modules"`
	ECCCorrectable   uint64     `json:"ecc_correctable"`
	ECCUncorrectable uint64     `json:"ecc_uncorrectable"`
	ECCSupported     bool       `json:"ecc_supported"`
	Status           string     `json:"status"`
	Issues           []string   `json:"issues,omitempty"`
}

// HWModule is one DIMM.
type HWModule struct {
	Locator      string `json:"locator"`
	Bank         string `json:"bank"`
	Manufacturer string `json:"manufacturer"`
	PartNumber   string `json:"part_number"`
	Serial       string `json:"serial"`
	Size         uint64 `json:"size"`
	SpeedMHz     int    `json:"speed_mhz"`
}

// HWAccelerator is a GPU or NPU.
type HWAccelerator struct {
	Name   string   `json:"name"`
	Vendor string   `json:"vendor"`
	Type   string   `json:"type"` // GPU or NPU
	Memory uint64   `json:"memory"`
	Driver string   `json:"driver"`
	Status string   `json:"status"`
	Issues []string `json:"issues,omitempty"`
}

// HWFan is a fan sensor, from hwmon, CIM or IPMI.
type HWFan struct {
	Name   string   `json:"name"`
	RPM    *float64 `json:"rpm"`
	MinRPM *float64 `json:"min_rpm"`
	Status string   `json:"status"`
	Source string   `json:"source"`
	Detail string   `json:"detail,omitempty"`
	Issues []string `json:"issues,omitempty"`
}

// HWPSU is a power supply status, usually from IPMI (HPE iLO) rather than
// the OS.
type HWPSU struct {
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Detail string   `json:"detail,omitempty"`
	Source string   `json:"source"`
	Issues []string `json:"issues,omitempty"`
}

// HWStorage is one physical disk's identity, health and wear.
type HWStorage struct {
	// ID is the reboot-stable identity (WWN / by-id alias). Name is the
	// kernel name (sda), kept only as a hint because it changes across boots.
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	// Device is the OS device path/number (sda, PhysicalDrive1); a hint only.
	Device            string   `json:"device,omitempty"`
	Model             string   `json:"model"`
	Serial            string   `json:"serial"`
	Media             string   `json:"media"`
	Bus               string   `json:"bus"`
	Size              uint64   `json:"size"`
	Health            string   `json:"health"`
	HealthNote        string   `json:"health_note,omitempty"`
	OperationalStatus string   `json:"operational_status"`
	TemperatureC      *float64 `json:"temperature_c"`
	WearPercent       *float64 `json:"wear_percent"`
	PowerOnHours      *uint64  `json:"power_on_hours"`
	ReadErrors        uint64   `json:"read_errors"`
	WriteErrors       uint64   `json:"write_errors"`
	MediaErrors       uint64   `json:"media_errors"`
	Status            string   `json:"status"`
	Issues            []string `json:"issues,omitempty"`
	// SMART lists every informative SMART reading of the disk.
	SMART []SmartMetric `json:"smart,omitempty"`
}

// SmartMetric is one SMART reading shown in the disk detail table. ATA
// attributes carry normalized/worst/threshold; NVMe and SAS counters only a
// value.
type SmartMetric struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name"`
	Value      string `json:"value"`
	Normalized *int   `json:"normalized,omitempty"`
	Worst      *int   `json:"worst,omitempty"`
	Threshold  *int   `json:"threshold,omitempty"`
	Status     string `json:"status,omitempty"` // ok | warning | critical
}

// Snapshot is one sample of every metric plus the active alerts.
type Snapshot struct {
	Ts        int64          `json:"ts"`
	Host      HostInfo       `json:"host"`
	CPU       CPUStat        `json:"cpu"`
	Memory    MemoryStat     `json:"memory"`
	Storage   StorageStat    `json:"storage"`
	Disk      DiskIOStat     `json:"disk"`
	Network   NetworkStat    `json:"network"`
	Ports     PortSummary    `json:"ports"`
	Processes ProcessSummary `json:"processes"`
	Apps      []AppStat      `json:"apps"`
	Baseline  Baseline       `json:"baseline"`
	Hardware  Hardware       `json:"hardware"`
	Diagnosis Diagnosis      `json:"diagnosis"`
	Alerts    []Alert        `json:"alerts"`
	Warnings  []string       `json:"warnings,omitempty"`

	// AllProcesses feeds the alert engine and is never sent to the UI.
	AllProcesses []ProcessInfo `json:"-"`
}

// Alert is one threshold breach. EndedAt zero means it is still active.
type Alert struct {
	ID        int64   `json:"id"`
	Key       string  `json:"key"`
	Kind      string  `json:"kind"`
	Severity  string  `json:"severity"`
	Message   string  `json:"message"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	StartedAt int64   `json:"started_at"`
	UpdatedAt int64   `json:"updated_at"`
	EndedAt   int64   `json:"ended_at"`
	Acked     bool    `json:"acked"`
}

// Active reports whether the alert has not ended.
func (a Alert) Active() bool { return a.EndedAt == 0 }

// HistoryPoint is one persisted sample, used by the charts.
type HistoryPoint struct {
	Ts           int64   `json:"ts"`
	CPU          float64 `json:"cpu"`
	RAM          float64 `json:"ram"`
	DiskRead     float64 `json:"disk_read"`
	DiskWrite    float64 `json:"disk_write"`
	NetRx        float64 `json:"net_rx"`
	NetTx        float64 `json:"net_tx"`
	Load1        float64 `json:"load1"`
	Ports        int     `json:"ports"`
	Procs        int     `json:"procs"`
	DiskReadLat  float64 `json:"disk_read_latency"`
	DiskWriteLat float64 `json:"disk_write_latency"`
	DiskQueue    float64 `json:"disk_queue"`
}

// NowMillis is the current Unix time in milliseconds.
func NowMillis() int64 { return time.Now().UnixMilli() }

// FormatBytes renders a byte count with binary units.
func FormatBytes(b float64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%.0f B", b)
	}
	div, exp := float64(unit), 0
	for n := b / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", b/div, "KMGTPE"[exp])
}

// FormatRate renders a bytes-per-second rate.
func FormatRate(bps float64) string {
	return FormatBytes(bps) + "/s"
}
