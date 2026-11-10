// Package prereq checks whether DMetric has what it needs to run smoothly on
// this machine and reports what an operator can fix.
package prereq

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/shirou/gopsutil/v4/disk"

	"dmetric.dalang.io/internal/model"
)

// Check is one runtime prerequisite.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // ok, info, warning, critical
	Detail string `json:"detail"`
	Action string `json:"action,omitempty"`
}

// Report is the prerequisite dashboard payload.
type Report struct {
	Status      string  `json:"status"`
	GeneratedAt int64   `json:"generated_at"`
	Checks      []Check `json:"checks"`
}

// Options is the running context the checks inspect.
type Options struct {
	DataDir  string
	Host     string
	Port     int
	Desktop  bool
	Repo     string
	Snapshot model.Snapshot
}

// Check runs every prerequisite check. None of them mutate application data.
func Run(ctx context.Context, opts Options) Report {
	checks := make([]Check, 0, 12)
	add := func(id, title, status, detail, action string) {
		checks = append(checks, Check{ID: id, Title: title, Status: status, Detail: detail, Action: action})
	}

	add("platform", "Platform",
		"ok", fmt.Sprintf("%s/%s, %s", runtime.GOOS, runtime.GOARCH, runtime.Version()), "")

	if err := writable(opts.DataDir); err != nil {
		add("data", "Direktori data", "critical",
			fmt.Sprintf("%s tidak dapat ditulis: %v", opts.DataDir, err),
			"Beri izin tulis ke direktori data atau jalankan dengan --data ke folder yang bisa ditulis.")
	} else {
		add("data", "Direktori data", "ok", opts.DataDir+" dapat ditulis", "")
	}

	if usage, err := disk.Usage(opts.DataDir); err == nil {
		free := float64(usage.Free)
		switch {
		case free < 512<<20:
			add("space", "Ruang disk", "critical",
				fmt.Sprintf("Bebas %s; aplikasi/database/update bisa gagal", model.FormatBytes(free)),
				"Kosongkan minimal 1 GiB pada volume data.")
		case free < 1<<30:
			add("space", "Ruang disk", "warning",
				fmt.Sprintf("Bebas %s; sisakan minimal 1 GiB", model.FormatBytes(free)),
				"Kosongkan ruang pada volume data.")
		default:
			add("space", "Ruang disk", "ok", fmt.Sprintf("Bebas %s", model.FormatBytes(free)), "")
		}
	} else {
		add("space", "Ruang disk", "warning", "Tidak dapat membaca ruang disk: "+err.Error(), "")
	}

	if isElevated() {
		add("privilege", "Hak akses", "ok", "Berjalan dengan hak administrator/root", "")
	} else {
		add("privilege", "Hak akses", "warning",
			"Berjalan sebagai user biasa; SMART, disk counter, SMART ATA, IPMI/ssacli bisa kosong",
			"Jalankan sebagai Administrator (Windows) atau root (Linux) untuk kesehatan hardware lengkap.")
	}

	if opts.Desktop {
		if mygo.WebViewAvailable() {
			add("webview", "WebView desktop", "ok", "WebView sistem tersedia", "")
		} else {
			add("webview", "WebView desktop", "warning",
				"WebView tidak tersedia; jendela desktop tidak bisa dibuka",
				"Instal WebView2 (Windows) / WebKitGTK 4.1 (Linux), atau jalankan dengan -no-window lalu buka lewat browser.")
		}
	} else {
		add("webview", "WebView desktop", "info", "Mode web-only (-no-window); WebView tidak dibutuhkan", "")
	}

	if opts.Host != "" {
		local := opts.Host == "127.0.0.1" || opts.Host == "localhost" || opts.Host == "::1"
		if local {
			add("bind", "Alamat listener", "ok", fmt.Sprintf("%s:%d (loopback)", opts.Host, opts.Port), "")
		} else {
			add("bind", "Alamat listener", "warning",
				fmt.Sprintf("Terbuka di %s:%d tanpa autentikasi", opts.Host, opts.Port),
				"Batasi dengan firewall/VPN atau reverse proxy berautentikasi.")
		}
	}

	tools := []string{"powershell.exe", "smartctl", "nvme", "ssacli", "hpssacli", "ipmitool", "lspci"}
	found := []string{}
	missing := []string{}
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err == nil {
			found = append(found, tool)
		} else if tool != "powershell.exe" || runtime.GOOS == "windows" {
			missing = append(missing, tool)
		}
	}
	sort.Strings(found)
	if len(found) == 0 {
		add("tools", "Tool hardware opsional", "info",
			"smartctl/nvme/ssacli/ipmitool tidak ditemukan; metrik dasar tetap berjalan",
			"Instal smartmontools, nvme-cli, dan tool vendor HPE untuk SMART/RAID/IPMI yang lebih lengkap.")
	} else {
		add("tools", "Tool hardware opsional", "ok",
			"Tersedia: "+strings.Join(found, ", ")+"; tidak ditemukan: "+strings.Join(missing, ", "), "")
	}

	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("powershell.exe"); err != nil {
			add("powershell", "PowerShell/CIM", "warning",
				"powershell.exe tidak ditemukan; kesehatan hardware Windows tidak lengkap",
				"Pastikan Windows PowerShell 5.1 tersedia dan tidak diblokir kebijakan.")
		} else {
			add("powershell", "PowerShell/CIM", "ok", "powershell.exe tersedia", "")
		}
		if len(opts.Snapshot.Disk.Disks) == 0 {
			add("diskio", "Counter disk", "warning",
				"Counter disk kosong; latensi/throughput bisa tidak terbaca",
				"Jalankan sebagai Administrator dan aktifkan 'diskperf -y'.")
		} else {
			add("diskio", "Counter disk", "ok", fmt.Sprintf("%d disk counter terbaca", len(opts.Snapshot.Disk.Disks)), "")
		}
	}

	if opts.Repo != "" {
		checkUpdateRepo(ctx, opts.Repo, add)
	}

	status := "ok"
	for _, check := range checks {
		switch check.Status {
		case "critical":
			status = "critical"
		case "warning":
			if status != "critical" {
				status = "warning"
			}
		}
	}
	return Report{Status: status, GeneratedAt: time.Now().UnixMilli(), Checks: checks}
}

func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".dmetric-prereq-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func checkUpdateRepo(ctx context.Context, repo string, add func(id, title, status, detail, action string)) {
	client := &http.Client{Timeout: 6 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repo, nil)
	if err != nil {
		add("update", "Rilis GitHub", "warning", err.Error(), "")
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dmetric-prereq")
	resp, err := client.Do(req)
	if err != nil {
		add("update", "Rilis GitHub", "warning",
			"GitHub tidak dapat dihubungi: "+err.Error(),
			"Pastikan server punya akses HTTPS keluar ke api.github.com untuk pembaruan.")
		return
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		add("update", "Rilis GitHub", "ok", repo+" dapat diakses", "")
	case http.StatusNotFound:
		add("update", "Rilis GitHub", "warning",
			repo+" belum publik atau belum punya rilis",
			"Buat repo/rilis publik agar tombol Update berfungsi; unduh manual tetap bisa.")
	default:
		add("update", "Rilis GitHub", "warning",
			fmt.Sprintf("GitHub mengembalikan HTTP %d", resp.StatusCode),
			"Coba lagi nanti atau gunakan update manual.")
	}
}

// isLoopbackHost is used by tests and callers that only have a host string.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
