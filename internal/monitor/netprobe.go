package monitor

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"dmetric.dalang.io/internal/model"
)

const (
	probeInterval = 5 * time.Second
	probeWindow   = 12
)

var pingRTT = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*ms`)

type probeState struct {
	results []bool
}

// networkProber measures the default gateway over ICMP and configured
// host:port targets over TCP. Latency and loss are what separate a busy
// network from a slow one.
type networkProber struct {
	targets    []string
	gateway    string
	lastDetect time.Time
	lastRun    time.Time
	latest     []model.ProbeResult
	states     map[string]*probeState
}

func newNetworkProber(targets []string) *networkProber {
	return &networkProber{
		targets: append([]string(nil), targets...),
		states:  map[string]*probeState{},
		latest:  []model.ProbeResult{},
	}
}

type probeJob struct {
	target string
	kind   string
	run    func(context.Context) (float64, error)
}

func (p *networkProber) probe(ctx context.Context) (string, []model.ProbeResult) {
	now := time.Now()
	if !p.lastRun.IsZero() && now.Sub(p.lastRun) < probeInterval {
		return p.gateway, p.latest
	}
	p.lastRun = now

	if p.gateway == "" && (p.lastDetect.IsZero() || now.Sub(p.lastDetect) > 10*time.Minute) {
		p.gateway = detectGateway(ctx)
		p.lastDetect = now
	}

	jobs := make([]probeJob, 0, len(p.targets)+1)
	if p.gateway != "" {
		gw := p.gateway
		jobs = append(jobs, probeJob{
			target: gw,
			kind:   "gateway",
			run: func(ctx context.Context) (float64, error) {
				return pingHost(ctx, gw)
			},
		})
	}
	for _, target := range p.targets {
		target := target
		jobs = append(jobs, probeJob{
			target: target,
			kind:   "tcp",
			run: func(ctx context.Context) (float64, error) {
				return tcpHandshake(ctx, target)
			},
		})
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make(map[string]model.ProbeResult, len(jobs))
	for _, job := range jobs {
		job := job
		wg.Add(1)
		go func() {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
			defer cancel()
			rtt, err := job.run(probeCtx)
			reachable := err == nil || probeReachable(err)
			result := model.ProbeResult{
				Target:    job.target,
				Kind:      job.kind,
				OK:        err == nil,
				Reachable: reachable,
				RTTms:     round1(rtt),
			}
			if !reachable {
				result.RTTms = 0
			}
			if err != nil {
				result.Error = shortProbeError(err)
			}
			mu.Lock()
			results[probeKey(job.kind, job.target)] = result
			mu.Unlock()
		}()
	}
	wg.Wait()

	ordered := make([]model.ProbeResult, 0, len(jobs))
	if p.gateway != "" {
		if result, ok := results[probeKey("gateway", p.gateway)]; ok {
			ordered = append(ordered, result)
		}
	}
	for _, target := range p.targets {
		if result, ok := results[probeKey("tcp", target)]; ok {
			ordered = append(ordered, result)
		}
	}
	// Loss state is updated here, after the goroutines finished, so the
	// maps and slices have a single writer.
	for i := range ordered {
		loss, attempts := p.record(ordered[i].Kind+"|"+ordered[i].Target, ordered[i].Reachable)
		ordered[i].LossPercent = round1(loss)
		ordered[i].Attempts = attempts
	}
	p.latest = ordered
	return p.gateway, ordered
}

func probeKey(kind, target string) string { return kind + "|" + target }

func (p *networkProber) record(key string, ok bool) (float64, int) {
	state := p.states[key]
	if state == nil {
		state = &probeState{}
		p.states[key] = state
	}
	state.results = append(state.results, ok)
	if len(state.results) > probeWindow {
		state.results = state.results[len(state.results)-probeWindow:]
	}
	// Three attempts are needed before a percentage means anything; the
	// first probes may run before the service (or this app's listener) is
	// ready.
	if len(state.results) < 3 {
		return 0, len(state.results)
	}
	failed := 0
	for _, result := range state.results {
		if !result {
			failed++
		}
	}
	return float64(failed) / float64(len(state.results)) * 100, len(state.results)
}

func tcpHandshake(ctx context.Context, target string) (float64, error) {
	start := time.Now()
	dialer := net.Dialer{Timeout: 1200 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp", target)
	elapsed := float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		// A refused or reset connection proves the host is reachable; the
		// elapsed time is still the network RTT. A timeout is not.
		if probeReachable(err) {
			return elapsed, err
		}
		return 0, err
	}
	_ = conn.Close()
	return elapsed, nil
}

func probeReachable(err error) bool {
	if err == nil {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "actively refused") ||
		strings.Contains(msg, "connection reset")
}

func pingHost(ctx context.Context, host string) (float64, error) {
	var args []string
	switch runtime.GOOS {
	case "windows":
		args = []string{"-n", "1", "-w", "1000", host}
	case "darwin":
		args = []string{"-c", "1", "-W", "1000", host}
	default:
		args = []string{"-c", "1", "-W", "1", host}
	}
	cmd := exec.CommandContext(ctx, "ping", args...)
	out, err := cmd.CombinedOutput()
	match := pingRTT.FindSubmatch(out)
	if len(match) < 2 {
		if err != nil {
			if ctx.Err() != nil {
				return 0, fmt.Errorf("timeout")
			}
			return 0, fmt.Errorf("tidak ada balasan")
		}
		return 0, fmt.Errorf("tidak ada balasan")
	}
	value, err := strconv.ParseFloat(string(match[1]), 64)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		value = 1
	}
	return value, nil
}

func shortProbeError(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"):
		return "timeout"
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "actively refused"):
		return "koneksi ditolak"
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "not known"):
		return "host tidak ditemukan"
	}
	text := err.Error()
	if len(text) > 70 {
		text = text[:70]
	}
	return text
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

func detectGateway(ctx context.Context) string {
	switch runtime.GOOS {
	case "windows":
		return gatewayWindows(ctx)
	case "darwin":
		return gatewayDarwin(ctx)
	case "linux":
		return gatewayLinux()
	}
	return ""
}

func gatewayLinux() string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&0x2 == 0 { // RTF_GATEWAY
			continue
		}
		raw, err := strconv.ParseUint(fields[2], 16, 32)
		if err != nil {
			continue
		}
		ip := make(net.IP, 4)
		binary.LittleEndian.PutUint32(ip, uint32(raw))
		if !ip.IsUnspecified() {
			return ip.String()
		}
	}
	return ""
}

func gatewayDarwin(ctx context.Context) string {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "route", "-n", "get", "default").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "gateway:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 && net.ParseIP(fields[1]) != nil {
					return fields[1]
				}
			}
		}
	}
	out, err = exec.CommandContext(cmdCtx, "netstat", "-rn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "default" && net.ParseIP(fields[1]) != nil {
			return fields[1]
		}
	}
	return ""
}

func gatewayWindows(ctx context.Context) string {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "route", "print", "-4").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "0.0.0.0" || fields[1] != "0.0.0.0" {
			continue
		}
		if net.ParseIP(fields[2]) != nil && fields[2] != "0.0.0.0" {
			return fields[2]
		}
	}
	return ""
}
