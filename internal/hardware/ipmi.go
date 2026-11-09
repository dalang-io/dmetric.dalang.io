package hardware

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dmetric.dalang.io/internal/model"
)

var rpmValue = regexp.MustCompile(`([0-9]+)\s*RPM`)

// collectIPMI reads fan and power-supply sensors through ipmitool when the
// tool and BMC access are available. HPE iLO is the usual source on servers.
func collectIPMI(ctx context.Context, notes *[]string) ([]model.HWFan, []model.HWPSU) {
	path, err := exec.LookPath("ipmitool")
	if err != nil {
		return nil, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	fans := []model.HWFan{}
	psus := []model.HWPSU{}
	failed := false

	if out, err := exec.CommandContext(runCtx, path, "sdr", "type", "Fan").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Split(line, "|")
			if len(fields) < 3 {
				continue
			}
			name := strings.TrimSpace(fields[0])
			reading := strings.TrimSpace(fields[1])
			status := strings.TrimSpace(fields[2])
			if name == "" {
				continue
			}
			fan := model.HWFan{Name: name, Status: status, Source: "IPMI", Detail: reading}
			if match := rpmValue.FindStringSubmatch(reading); len(match) == 2 {
				if rpm, err := strconv.ParseFloat(match[1], 64); err == nil {
					fan.RPM = &rpm
				}
			}
			fans = append(fans, fan)
		}
	} else {
		failed = true
	}

	types := []string{"Power Supply", "Power Unit"}
	for _, sensorType := range types {
		out, err := exec.CommandContext(runCtx, path, "sdr", "type", sensorType).Output()
		if err != nil {
			failed = true
			continue
		}
		psus = append(psus, parsePSULines(string(out))...)
	}
	if len(psus) == 0 {
		// Some BMCs expose PSUs under another type; scan all sensor names.
		if out, err := exec.CommandContext(runCtx, path, "sdr").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				lower := strings.ToLower(line)
				if !strings.Contains(lower, "psu") && !strings.Contains(lower, "power supply") {
					continue
				}
				psus = append(psus, parsePSULines(line)...)
			}
		}
	}

	if failed && len(fans) == 0 && len(psus) == 0 {
		*notes = append(*notes, "ipmitool ada tetapi BMC tidak dapat dibaca (butuh driver/root)")
	}
	return fans, psus
}

func parsePSULines(output string) []model.HWPSU {
	out := []model.HWPSU{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "|")
		if len(fields) < 3 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		if name == "" {
			continue
		}
		out = append(out, model.HWPSU{
			Name:   name,
			Status: strings.TrimSpace(fields[2]),
			Detail: strings.TrimSpace(fields[1]),
			Source: "IPMI",
		})
	}
	return out
}
