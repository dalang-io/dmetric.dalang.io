package hardware

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dmetric.dalang.io/internal/model"
)

// HPE Smart Array support through ssacli (the Smart Storage Administrator
// CLI). A Smart Array in RAID mode hides its physical drives from the OS, so
// neither SMART nor Get-PhysicalDisk can see them; ssacli reports drive,
// logical drive, controller, cache and battery/capacitor health instead.
// Parsing is OS-independent; only the lookup of the executable differs.

type ssaController struct {
	Title  string // "Smart Array P440ar in Slot 0 (Embedded)"
	Model  string // "Smart Array P440ar"
	Slot   string
	Fields map[string]string
	Arrays map[string]map[string]string
	LDs    []*ssaLogical
	PDs    []*ssaPhysical
}

type ssaLogical struct {
	Array  string
	Number string
	Fields map[string]string
}

type ssaPhysical struct {
	Array    string // "" for unassigned drives
	Location string // "1I:1:1" (port:box:bay)
	Fields   map[string]string
}

var ssaControllerRe = regexp.MustCompile(`^(.+?)\s+in\s+Slot\s+(\S+)`)

// parseSSACLI parses `ssacli ctrl all show config detail`. The output is an
// indented outline: controller, then arrays holding logical drives and
// physicaldrive blocks, then "Unassigned" physical drives. Lines are
// "Key: value" and belong to the innermost block opened above them.
func parseSSACLI(text string) []*ssaController {
	var ctrls []*ssaController
	var ctrl *ssaController
	var fields map[string]string
	array := ""

	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		lower := strings.ToLower(line)

		if indent == 0 && !strings.Contains(line, ":") {
			if m := ssaControllerRe.FindStringSubmatch(line); m != nil {
				ctrl = &ssaController{
					Title: line, Model: strings.TrimSpace(m[1]), Slot: m[2],
					Fields: map[string]string{}, Arrays: map[string]map[string]string{},
				}
				ctrls = append(ctrls, ctrl)
				fields = ctrl.Fields
				array = ""
			}
			continue
		}
		if ctrl == nil {
			continue // banner or error text before the first controller
		}

		switch {
		case strings.HasPrefix(lower, "array:"):
			array = strings.TrimSpace(line[len("array:"):])
			ctrl.Arrays[array] = map[string]string{}
			fields = ctrl.Arrays[array]
		case strings.HasPrefix(lower, "logical drive:"):
			ld := &ssaLogical{Array: array, Number: strings.TrimSpace(line[len("logical drive:"):]), Fields: map[string]string{}}
			ctrl.LDs = append(ctrl.LDs, ld)
			fields = ld.Fields
		case strings.HasPrefix(lower, "physicaldrive "):
			pd := &ssaPhysical{Array: array, Location: strings.TrimSpace(line[len("physicaldrive "):]), Fields: map[string]string{}}
			ctrl.PDs = append(ctrl.PDs, pd)
			fields = pd.Fields
		case lower == "unassigned":
			array = ""
			fields = ctrl.Fields // keys until the next block are not meaningful
		default:
			if key, value, ok := strings.Cut(line, ":"); ok {
				fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}
		}
	}
	return ctrls
}

// ssaVerdict maps an ssacli status string to a health word and an
// operational status understood by assessStorage.
func ssaVerdict(status string) (health, operational string) {
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case s == "" || s == "ok":
		return "Healthy", ""
	case strings.Contains(s, "predictive"):
		return "Unhealthy", "Predictive failure"
	case strings.Contains(s, "interim recovery"):
		// Redundancy is already lost.
		return "Warning", "Degraded (" + status + ")"
	case strings.Contains(s, "fail"), strings.Contains(s, "missing"), strings.Contains(s, "offline"):
		return "Unhealthy", status
	case strings.Contains(s, "rebuild"), strings.Contains(s, "recover"),
		strings.Contains(s, "erase"), strings.Contains(s, "expand"), strings.Contains(s, "queued"):
		return "Warning", "Stressed (" + status + ")"
	}
	return "Warning", status
}

// ssaComponentSeverity rates controller / cache / battery status strings.
func ssaComponentSeverity(status string) string {
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case s == "ok":
		return "ok"
	case s == "" || strings.Contains(s, "not configured") || strings.Contains(s, "not present") ||
		s == "n/a" || strings.Contains(s, "not applicable"):
		return "ok" // component absent by design; shown, not alarming
	case strings.Contains(s, "fail"), strings.Contains(s, "permanently"), strings.Contains(s, "degraded"):
		return "critical"
	}
	return "warning" // charging, temporarily disabled, ...
}

// parseSSASize reads sizes such as "300 GB", "1.9 TB" (decimal units).
func parseSSASize(value string) uint64 {
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return 0
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || n < 0 {
		return 0
	}
	mult := map[string]float64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15}
	m, ok := mult[strings.ToUpper(fields[1])]
	if !ok {
		return 0
	}
	return uint64(n * m)
}

var ssaNumberRe = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)

func ssaNumber(value string) (float64, bool) {
	m := ssaNumberRe.FindString(value)
	if m == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(m, 64)
	return v, err == nil
}

// ssaLifeKeys lists SSD life/endurance keys; their wording varies by ssacli
// version ("Usage remaining", "Estimated Life Remaining based on workload").
func ssaLifeKeys(f map[string]string) []string {
	var keys []string
	for key := range f {
		lk := strings.ToLower(key)
		if strings.Contains(lk, "life remaining") || strings.Contains(lk, "usage remaining") || strings.Contains(lk, "endurance remaining") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func collapseSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

func ssaMetric(name, value, severity string) model.SmartMetric {
	return model.SmartMetric{Name: name, Value: value, Status: severity}
}

// ssaPhysicalToStorage turns one physicaldrive block into a disk row.
func ssaPhysicalToStorage(c *ssaController, pd *ssaPhysical) model.HWStorage {
	f := pd.Fields
	modelName := collapseSpaces(f["Model"])
	serial := f["Serial Number"]
	d := model.HWStorage{
		Name:   pd.Location,
		Device: fmt.Sprintf("%s · %s", c.Model, pd.Location),
		Model:  modelName,
		Serial: serial,
		Bus:    "RAID member",
		Size:   parseSSASize(f["Size"]),
	}
	if wwid := strings.ToLower(strings.TrimSpace(f["WWID"])); isHex(wwid) && !allZeroHex(wwid) {
		d.ID = "naa." + wwid
	} else if serial != "" {
		d.ID = strings.NewReplacer(" ", "_", "/", "_").Replace(modelName + "_" + serial)
	} else {
		d.ID = fmt.Sprintf("slot%s-%s", c.Slot, pd.Location)
	}
	iface := f["Interface Type"]
	switch {
	case strings.Contains(strings.ToLower(iface), "solid state"):
		d.Media = "SSD"
	case iface != "":
		d.Media = "HDD"
	}
	if rpm := f["Rotational Speed"]; rpm == "0" {
		d.Media = "SSD"
	}
	d.Health, d.OperationalStatus = ssaVerdict(f["Status"])
	if v, ok := ssaNumber(f["Current Temperature (C)"]); ok && v > 0 {
		d.TemperatureC = &v
	}
	if v, ok := ssaNumber(f["Power On Hours"]); ok {
		h := uint64(v)
		d.PowerOnHours = &h
	}
	lifeKeys := ssaLifeKeys(f)
	for _, key := range lifeKeys {
		if v, ok := ssaNumber(f[key]); ok && v >= 0 && v <= 100 {
			wear := 100 - v
			d.WearPercent = &wear
			break
		}
	}

	status := "ok"
	if d.Health != "Healthy" {
		status = "warning"
		if d.Health == "Unhealthy" {
			status = "critical"
		}
	}
	d.SMART = append(d.SMART, ssaMetric("Status", f["Status"], status))
	for _, key := range []string{"Drive Type", "Interface Type", "Rotational Speed", "Firmware Revision",
		"Current Temperature (C)", "Maximum Temperature (C)", "Power On Hours", "Logical/Physical Block Size",
		"Drive exposed to OS", "Encryption Status", "Sanitize Erase Supported", "PHY Count"} {
		if v := f[key]; v != "" {
			d.SMART = append(d.SMART, ssaMetric(key, v, "ok"))
		}
	}
	for _, key := range lifeKeys {
		d.SMART = append(d.SMART, ssaMetric(key, f[key], "ok"))
	}
	location := "Unassigned"
	if pd.Array != "" {
		location = "Array " + pd.Array
	}
	d.SMART = append(d.SMART, ssaMetric("Array", location, "ok"))
	return d
}

// ssaControllerToStorage summarises controller, cache and battery health.
func ssaControllerToStorage(c *ssaController) model.HWStorage {
	f := c.Fields
	d := model.HWStorage{
		ID:     "ctrl-" + strings.ToLower(strings.NewReplacer(" ", "_").Replace(firstNonEmpty(f["Serial Number"], "slot"+c.Slot))),
		Name:   c.Title,
		Device: "Slot " + c.Slot,
		Model:  c.Model,
		Serial: f["Serial Number"],
		Bus:    "RAID controller",
		Media:  "Controller",
	}
	worst := "ok"
	rank := map[string]int{"ok": 0, "warning": 1, "critical": 2}
	for _, key := range []string{"Controller Status", "Cache Status", "Battery/Capacitor Status", "Cache Module Status"} {
		value, present := f[key]
		if !present {
			continue
		}
		sev := ssaComponentSeverity(value)
		d.SMART = append(d.SMART, ssaMetric(key, value, sev))
		if sev != "ok" {
			d.Issues = append(d.Issues, fmt.Sprintf("%s: %s", key, value))
		}
		if rank[sev] > rank[worst] {
			worst = sev
		}
	}
	for _, key := range []string{"Controller Mode", "Firmware Version", "Hardware Revision", "Cache Ratio",
		"Total Cache Size", "Rebuild Priority", "Expand Priority", "Surface Scan Delay", "Driver Name", "Driver Version"} {
		if v := f[key]; v != "" {
			d.SMART = append(d.SMART, ssaMetric(key, v, "ok"))
		}
	}
	switch worst {
	case "critical":
		d.Health = "Unhealthy"
	case "warning":
		d.Health = "Warning"
	default:
		d.Health = "Healthy"
	}
	return d
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func ssaRAIDLevel(faultTolerance string) string {
	ft := strings.ToUpper(strings.TrimSpace(faultTolerance))
	if ft == "" {
		return "RAID"
	}
	ft = strings.NewReplacer("ADM", " ADM", "(ADG)", "").Replace(ft)
	return "RAID " + strings.TrimSpace(ft)
}

// ssaApplyLogical enriches a disk (the OS's view of a logical drive) or a new
// row with the logical drive's RAID level and health.
func ssaApplyLogical(d *model.HWStorage, c *ssaController, ld *ssaLogical) {
	f := ld.Fields
	d.Bus = "RAID"
	d.Media = ssaRAIDLevel(f["Fault Tolerance"])
	if d.Size == 0 {
		d.Size = parseSSASize(f["Size"])
	}
	health, op := ssaVerdict(f["Status"])
	d.Health, d.OperationalStatus = health, op
	d.HealthNote = ""
	status := "ok"
	if health != "Healthy" {
		status = "warning"
		if health == "Unhealthy" || strings.Contains(strings.ToLower(op), "degraded") {
			status = "critical"
		}
	}
	members := 0
	for _, pd := range c.PDs {
		if pd.Array == ld.Array {
			members++
		}
	}
	d.SMART = append(d.SMART, ssaMetric("Status", f["Status"], status))
	d.SMART = append(d.SMART, ssaMetric("RAID level", d.Media, "ok"))
	if members > 0 {
		d.SMART = append(d.SMART, ssaMetric("Member drives", strconv.Itoa(members), "ok"))
	}
	for _, key := range []string{"Size", "Strip Size", "Full Stripe Size", "Caching", "LD Acceleration Method",
		"Parity Initialization Status", "Unique Identifier", "Logical Drive Label"} {
		if v := f[key]; v != "" {
			d.SMART = append(d.SMART, ssaMetric(key, v, "ok"))
		}
	}
	if arr := c.Arrays[ld.Array]; arr != nil {
		for _, key := range []string{"Interface Type", "Array Type", "Smart Path"} {
			if v := arr[key]; v != "" {
				d.SMART = append(d.SMART, ssaMetric("Array "+ld.Array+" "+key, v, "ok"))
			}
		}
	}
}

func hexOnly(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ssaFindDisk finds the OS disk backing a logical drive by its unique
// identifier, which Windows reports as the disk UniqueId and Linux as the
// wwn-/scsi-3 by-id alias.
func ssaFindDisk(storage []model.HWStorage, uniqueID string) int {
	uid := hexOnly(uniqueID)
	if len(uid) < 16 {
		return -1
	}
	for i := range storage {
		if strings.Contains(hexOnly(storage[i].ID), uid) {
			return i
		}
	}
	return -1
}

// applySSACLI merges parsed controllers into the hardware storage list and
// returns whether anything was merged.
func applySSACLI(hw *model.Hardware, ctrls []*ssaController) bool {
	if len(ctrls) == 0 {
		return false
	}
	for _, c := range ctrls {
		hw.Storage = append(hw.Storage, ssaControllerToStorage(c))
		for _, ld := range c.LDs {
			if i := ssaFindDisk(hw.Storage, ld.Fields["Unique Identifier"]); i >= 0 {
				ssaApplyLogical(&hw.Storage[i], c, ld)
				continue
			}
			row := model.HWStorage{
				Name:   fmt.Sprintf("Logical drive %s", ld.Number),
				Device: fmt.Sprintf("%s · Array %s", c.Model, ld.Array),
				Model:  "Logical drive",
			}
			if uid := hexOnly(ld.Fields["Unique Identifier"]); len(uid) >= 16 {
				row.ID = "naa." + uid
			} else {
				row.ID = fmt.Sprintf("slot%s-ld%s", c.Slot, ld.Number)
			}
			ssaApplyLogical(&row, c, ld)
			hw.Storage = append(hw.Storage, row)
		}
		for _, pd := range c.PDs {
			hw.Storage = append(hw.Storage, ssaPhysicalToStorage(c, pd))
		}
	}
	return true
}

func hasSSACLIData(storage []model.HWStorage) bool {
	for _, d := range storage {
		if d.Bus == "RAID controller" {
			return true
		}
	}
	return false
}

// findSSACLI locates the executable on PATH or in the installers' default
// directories.
func findSSACLI() string {
	for _, name := range []string{"ssacli", "ssacli.exe", "hpssacli", "hpssacli.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	candidates := []string{
		`C:\Program Files\Smart Storage Administrator\ssacli\bin\ssacli.exe`,
		`C:\Program Files\Smart Storage Administrator\ssacli\bin\x64\ssacli.exe`,
		`C:\Program Files (x86)\Smart Storage Administrator\ssacli\bin\ssacli.exe`,
		`C:\Program Files\HP\HP Smart Storage Administrator\hpssacli\bin\hpssacli.exe`,
		"/usr/sbin/ssacli",
		"/opt/smartstorageadmin/ssacli/bin/ssacli",
		"/opt/hp/hpssacli/bin/hpssacli",
	}
	for _, p := range candidates {
		if info, err := os.Stat(filepath.FromSlash(p)); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// mergeSSACLI runs ssacli when installed and merges the result. It is a no-op
// on machines without a Smart Array.
func mergeSSACLI(ctx context.Context, hw *model.Hardware) {
	path := findSSACLI()
	if path == "" {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, path, "ctrl", "all", "show", "config", "detail").Output()
	if len(out) == 0 {
		if err != nil {
			hw.Notes = append(hw.Notes, "ssacli gagal: "+shortError(err))
		}
		return
	}
	ctrls := parseSSACLI(string(out))
	if len(ctrls) == 0 {
		// "Error: No controllers detected" on non-HPE machines is normal.
		return
	}
	applySSACLI(hw, ctrls)
	hw.Notes = append(hw.Notes, "Status RAID, drive fisik, cache, dan baterai dibaca via HPE ssacli")
}
