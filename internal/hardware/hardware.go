// Package hardware collects motherboard, CPU, RAM and physical disk health.
//
// It is deliberately independent of dcheck (which is Rust and targets
// Linux/FreeBSD/macOS): this package uses the same checks through each OS's
// own interfaces — CIM/PowerShell on Windows, sysfs/EDAC/smartctl on Linux,
// system_profiler on macOS.
package hardware

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"dmetric.dalang.io/internal/model"
)

// Component status values.
const (
	StatusOK       = "ok"
	StatusWarning  = "warning"
	StatusCritical = "critical"
	StatusUnknown  = "unknown"
)

// Collect reads the hardware inventory and health for the current OS.
func Collect(ctx context.Context) model.Hardware {
	hw := collect(ctx)
	if hw.CollectedAt == 0 {
		hw.CollectedAt = time.Now().UnixMilli()
	}
	return hw
}

func empty(source string, notes ...string) model.Hardware {
	return model.Hardware{
		Source:       source,
		CollectedAt:  time.Now().UnixMilli(),
		Status:       StatusUnknown,
		Accelerators: []model.HWAccelerator{},
		Fans:         []model.HWFan{},
		PSUs:         []model.HWPSU{},
		Storage:      []model.HWStorage{},
		Issues:       []string{},
		Notes:        notes,
	}
}

func finalize(h *model.Hardware) {
	if h.Storage == nil {
		h.Storage = []model.HWStorage{}
	}
	if h.Accelerators == nil {
		h.Accelerators = []model.HWAccelerator{}
	}
	if h.Fans == nil {
		h.Fans = []model.HWFan{}
	}
	if h.PSUs == nil {
		h.PSUs = []model.HWPSU{}
	}
	h.Issues = []string{}

	for i := range h.Storage {
		assessStorage(&h.Storage[i])
	}
	for i := range h.Accelerators {
		assessAccelerator(&h.Accelerators[i])
	}
	for i := range h.Fans {
		assessFan(&h.Fans[i])
	}
	for i := range h.PSUs {
		assessPSU(&h.PSUs[i])
	}

	if h.System.Status == "" {
		if h.System.Vendor != "" || h.System.Product != "" || h.System.BIOSVersion != "" {
			h.System.Status = StatusOK
		} else {
			h.System.Status = StatusUnknown
		}
	}
	if h.System.WHEAErrors > 0 {
		addSystemIssue(&h.System, StatusWarning,
			fmt.Sprintf("%d WHEA hardware error(s) dalam 7 hari; cek RAM/CPU/PCIe dan log sistem", h.System.WHEAErrors))
	}

	if h.CPU.Status == "" {
		if h.CPU.Model != "" {
			h.CPU.Status = StatusOK
		} else {
			h.CPU.Status = StatusUnknown
		}
	}
	if h.CPU.TemperatureC != nil {
		temp := *h.CPU.TemperatureC
		switch {
		case temp >= 95:
			addCPUIssue(&h.CPU, StatusCritical, fmt.Sprintf("suhu CPU %.0f°C (kritis)", temp))
		case temp >= 85:
			addCPUIssue(&h.CPU, StatusWarning, fmt.Sprintf("suhu CPU %.0f°C tinggi", temp))
		}
	}

	if h.Memory.Status == "" {
		if h.Memory.Total > 0 {
			h.Memory.Status = StatusOK
		} else {
			h.Memory.Status = StatusUnknown
		}
	}
	if h.Memory.ECCUncorrectable > 0 {
		addMemoryIssue(&h.Memory, StatusCritical,
			fmt.Sprintf("%d ECC uncorrectable error; RAM/module kemungkinan rusak", h.Memory.ECCUncorrectable))
	} else if h.Memory.ECCCorrectable > 0 {
		addMemoryIssue(&h.Memory, StatusWarning,
			fmt.Sprintf("%d ECC correctable error; pantau dan cek modul RAM", h.Memory.ECCCorrectable))
	}

	worst := StatusOK
	known := false
	for _, status := range []string{h.System.Status, h.CPU.Status, h.Memory.Status} {
		worst, known = mergeStatus(worst, known, status)
	}
	for i := range h.Storage {
		worst, known = mergeStatus(worst, known, h.Storage[i].Status)
	}
	for i := range h.Accelerators {
		worst, known = mergeStatus(worst, known, h.Accelerators[i].Status)
	}
	for i := range h.Fans {
		worst, known = mergeStatus(worst, known, h.Fans[i].Status)
	}
	for i := range h.PSUs {
		worst, known = mergeStatus(worst, known, h.PSUs[i].Status)
	}
	if !known {
		worst = StatusUnknown
	}
	h.Status = worst

	addComponentIssues := func(name string, issues []string) {
		for _, issue := range issues {
			h.Issues = append(h.Issues, name+": "+issue)
		}
	}
	addComponentIssues("Motherboard", h.System.Issues)
	addComponentIssues("CPU", h.CPU.Issues)
	addComponentIssues("RAM", h.Memory.Issues)
	for i := range h.Storage {
		addComponentIssues(h.Storage[i].Name, h.Storage[i].Issues)
	}
	for i := range h.Accelerators {
		addComponentIssues(h.Accelerators[i].Name, h.Accelerators[i].Issues)
	}
	for i := range h.Fans {
		addComponentIssues(h.Fans[i].Name, h.Fans[i].Issues)
	}
	for i := range h.PSUs {
		addComponentIssues(h.PSUs[i].Name, h.PSUs[i].Issues)
	}
}

func assessAccelerator(a *model.HWAccelerator) {
	a.Issues = nil
	status := statusFromText(a.Status)
	if status == StatusWarning || status == StatusCritical {
		a.Status = status
		a.Issues = append(a.Issues, "status: "+a.Status)
	} else if a.Status == "" {
		a.Status = StatusOK
	} else {
		a.Status = StatusOK
	}
}

func assessFan(f *model.HWFan) {
	f.Issues = nil
	rawStatus := f.Status
	status := statusFromText(rawStatus)
	if f.RPM != nil {
		switch {
		case *f.RPM <= 0:
			status = worse(status, StatusCritical)
			f.Issues = append(f.Issues, "RPM 0: fan berhenti atau tidak terbaca")
		case f.MinRPM != nil && *f.MinRPM > 0 && *f.RPM < *f.MinRPM:
			status = worse(status, StatusWarning)
			f.Issues = append(f.Issues, fmt.Sprintf("%.0f RPM di bawah minimum %.0f", *f.RPM, *f.MinRPM))
		case status == StatusUnknown:
			status = StatusOK
		}
	}
	if (status == StatusWarning || status == StatusCritical) && len(f.Issues) == 0 {
		detail := strings.TrimSpace(rawStatus)
		if detail == "" {
			detail = status
		}
		f.Issues = append(f.Issues, "status sensor: "+detail)
	}
	f.Status = status
}

func assessPSU(p *model.HWPSU) {
	p.Issues = nil
	status := statusFromText(p.Status)
	p.Status = status
	if status == StatusWarning || status == StatusCritical {
		detail := p.Detail
		if detail == "" {
			detail = "status " + p.Status
		}
		p.Issues = append(p.Issues, detail)
	}
}

func statusFromText(text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return StatusUnknown
	}
	// IPMI status fields are short codes (cr, nc, nr, lnr, ...). Match them
	// as whole tokens: substring matching would read "no redundancy" as
	// "nc".
	codeWarning, codeCritical := false, false
	for _, token := range strings.FieldsFunc(t, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		switch token {
		case "nc", "lnc", "unc":
			codeWarning = true
		case "cr", "lcr", "ucr", "nr", "lnr", "unr":
			codeCritical = true
		}
	}
	if codeCritical {
		return StatusCritical
	}
	if containsAny(t, "non-critical", "warning", "degraded", "stressed", "predictive", "absent") {
		return StatusWarning
	}
	if containsAny(t, "critical", "failed", "fail", "unhealthy", "error", "non-recoverable", "lost", "no redundancy", "not ok", "not-ok") {
		return StatusCritical
	}
	if codeWarning {
		return StatusWarning
	}
	if containsAny(t, "ok", "healthy", "normal", "present", "good") {
		return StatusOK
	}
	return StatusUnknown
}

func assessStorage(d *model.HWStorage) {
	status := StatusUnknown
	issues := append([]string(nil), d.Issues...)
	add := func(sev, message string) {
		status = worse(status, sev)
		issues = append(issues, message)
	}

	health := strings.ToLower(strings.TrimSpace(d.Health))
	switch {
	case containsAny(health, "unhealthy", "failed", "failing", "critical", "bad"):
		status = worse(status, StatusCritical)
	case containsAny(health, "warning", "degraded", "predictive", "stressed", "lost communication", "no redundancy"):
		status = worse(status, StatusWarning)
	case health != "" && !containsAny(health, "unknown", "not supported", "n/a"):
		status = worse(status, StatusOK)
	}
	operational := strings.ToLower(d.OperationalStatus)
	switch {
	case containsAny(operational, "failed", "lost communication", "no redundancy", "degraded", "in error"):
		// Degraded on a RAID volume means redundancy is already lost.
		add(StatusCritical, "status operasional: "+d.OperationalStatus)
	case containsAny(operational, "predictive failure", "stressed", "warning"):
		add(StatusWarning, "status operasional: "+d.OperationalStatus)
	}

	if d.WearPercent != nil {
		switch {
		case *d.WearPercent >= 90:
			add(StatusCritical, fmt.Sprintf("wear %.0f%%; endurance hampir habis", *d.WearPercent))
		case *d.WearPercent >= 80:
			add(StatusWarning, fmt.Sprintf("wear %.0f%%; siapkan penggantian", *d.WearPercent))
		}
	}
	if d.TemperatureC != nil && *d.TemperatureC > 0 {
		temp := *d.TemperatureC
		switch {
		case temp >= 70:
			add(StatusCritical, fmt.Sprintf("suhu %.0f°C (kritis)", temp))
		case temp >= 60:
			add(StatusWarning, fmt.Sprintf("suhu %.0f°C tinggi", temp))
		}
	}
	if d.ReadErrors > 0 || d.WriteErrors > 0 {
		add(StatusWarning, fmt.Sprintf("%d read / %d write error", d.ReadErrors, d.WriteErrors))
	}
	if d.MediaErrors > 0 {
		add(StatusWarning, fmt.Sprintf("%d media error", d.MediaErrors))
	}
	if len(issues) > 0 && statusRank(status) < statusRank(StatusWarning) {
		status = StatusWarning
	}
	if status == "" {
		status = StatusUnknown
	}
	d.Issues = issues
	d.Status = status
}

// The component types differ, so small typed helpers keep call sites
// readable.
func addSystemIssue(c *model.HWSystem, sev, message string) {
	c.Status = worse(c.Status, sev)
	c.Issues = append(c.Issues, message)
}

func addCPUIssue(c *model.HWCPU, sev, message string) {
	c.Status = worse(c.Status, sev)
	c.Issues = append(c.Issues, message)
}

func addMemoryIssue(c *model.HWMemory, sev, message string) {
	c.Status = worse(c.Status, sev)
	c.Issues = append(c.Issues, message)
}

func worse(a, b string) string {
	if statusRank(b) > statusRank(a) {
		return b
	}
	return a
}

func statusRank(status string) int {
	switch status {
	case StatusCritical:
		return 3
	case StatusWarning:
		return 2
	case StatusOK:
		return 1
	default:
		return 0
	}
}

func mergeStatus(worst string, known bool, status string) (string, bool) {
	if status == StatusOK || status == StatusWarning || status == StatusCritical {
		known = true
	}
	return worse(worst, status), known
}

func firstInt(value string) int {
	for _, field := range strings.Fields(value) {
		if n, err := strconv.Atoi(field); err == nil {
			return n
		}
	}
	return 0
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
