package hardware

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"dmetric.dalang.io/internal/model"
)

// Parsing helpers for SMART data. They are OS-independent so they can be
// tested anywhere; the Linux collector feeds them smartctl / nvme-cli JSON or
// the in-process ATA reader (ata.go).

type smartPayload struct {
	ModelName    string `json:"model_name"`
	SerialNumber string `json:"serial_number"`
	RotationRate *int   `json:"rotation_rate"`
	UserCapacity *struct {
		Bytes uint64 `json:"bytes"`
	} `json:"user_capacity"`
	SmartStatus *struct {
		Passed *bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current *float64 `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours *uint64 `json:"hours"`
	} `json:"power_on_time"`
	PowerCycleCount *uint64 `json:"power_cycle_count"`
	// smartctl nests NVMe counters under nvme_smart_health_information_log.
	NVMeLog *struct {
		CriticalWarning *uint64   `json:"critical_warning"`
		Temperature     *float64  `json:"temperature"`
		AvailableSpare  *float64  `json:"available_spare"`
		SpareThreshold  *float64  `json:"available_spare_threshold"`
		PercentageUsed  *float64  `json:"percentage_used"`
		DataUnitsRead   *uint64   `json:"data_units_read"`
		DataUnitsWrite  *uint64   `json:"data_units_written"`
		HostReads       *uint64   `json:"host_reads"`
		HostWrites      *uint64   `json:"host_writes"`
		ControllerBusy  *uint64   `json:"controller_busy_time"`
		PowerCycles     *uint64   `json:"power_cycles"`
		PowerOnHours    *uint64   `json:"power_on_hours"`
		UnsafeShutdowns *uint64   `json:"unsafe_shutdowns"`
		MediaErrors     *uint64   `json:"media_errors"`
		ErrorLogEntries *uint64   `json:"num_err_log_entries"`
		WarningTempTime *uint64   `json:"warning_temp_time"`
		CriticalTemp    *uint64   `json:"critical_comp_time"`
		Sensors         []float64 `json:"temperature_sensors"`
	} `json:"nvme_smart_health_information_log"`
	GrownDefects       *uint64 `json:"scsi_grown_defect_list"`
	ATASmartAttributes *struct {
		Table []struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Value  int    `json:"value"`
			Worst  int    `json:"worst"`
			Thresh int    `json:"thresh"`
			Raw    struct {
				Value  *uint64 `json:"value"`
				String string  `json:"string"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
}

// isRAIDVirtualDisk recognises hardware-RAID logical volumes by their model
// string (Dell PERC, LSI MegaRAID, HPE Smart Array).
func isRAIDVirtualDisk(model string) bool {
	upper := strings.ToUpper(model)
	for _, marker := range []string{"PERC ", "MEGARAID", "LOGICAL VOLUME", "SMART ARRAY", "VIRTUAL DISK"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func applySMART(disk *model.HWStorage, payload *smartPayload) {
	if disk.Serial == "" && payload.SerialNumber != "" {
		disk.Serial = payload.SerialNumber
	}
	if payload.SmartStatus != nil && payload.SmartStatus.Passed != nil {
		if *payload.SmartStatus.Passed {
			disk.Health = "Healthy"
		} else {
			disk.Health = "Unhealthy"
		}
	}
	if payload.Temperature != nil && payload.Temperature.Current != nil && *payload.Temperature.Current > 0 {
		disk.TemperatureC = payload.Temperature.Current
	}
	if payload.PowerOnTime != nil && payload.PowerOnTime.Hours != nil {
		disk.PowerOnHours = payload.PowerOnTime.Hours
	}
	if log := payload.NVMeLog; log != nil {
		h := nvmeHealth{
			CriticalWarning: log.CriticalWarning, AvailableSpare: log.AvailableSpare,
			SpareThreshold: log.SpareThreshold, PercentUsed: log.PercentageUsed,
			DataUnitsRead: log.DataUnitsRead, DataUnitsWritten: log.DataUnitsWrite,
			HostReads: log.HostReads, HostWrites: log.HostWrites, ControllerBusy: log.ControllerBusy,
			PowerCycles: log.PowerCycles, PowerOnHours: log.PowerOnHours,
			UnsafeShutdowns: log.UnsafeShutdowns, MediaErrors: log.MediaErrors,
			ErrorLogEntries: log.ErrorLogEntries, WarningTempTime: log.WarningTempTime,
			CriticalTempTime: log.CriticalTemp, Sensors: log.Sensors,
		}
		if disk.TemperatureC != nil {
			h.TemperatureC = disk.TemperatureC
		} else if log.Temperature != nil {
			h.TemperatureC = log.Temperature
		}
		applyNVMeHealth(disk, h)
	}
	if payload.GrownDefects != nil {
		disk.SMART = append(disk.SMART, metric("", "Grown defect list (SAS)", fmt.Sprint(*payload.GrownDefects),
			statusIf(*payload.GrownDefects > 0, "warning")))
		if *payload.GrownDefects > 0 {
			disk.Issues = append(disk.Issues, fmt.Sprintf("%d grown defect (SAS)", *payload.GrownDefects))
		}
	}
	if payload.PowerCycleCount != nil {
		disk.SMART = append(disk.SMART, metric("", "Power cycle count", fmt.Sprint(*payload.PowerCycleCount), "ok"))
	}
	if t := payload.ATASmartAttributes; t != nil {
		attrs := make([]ataAttribute, 0, len(t.Table))
		for _, row := range t.Table {
			a := ataAttribute{ID: row.ID, Name: row.Name, Current: uint8(row.Value), Worst: uint8(row.Worst), Threshold: row.Thresh, HasThreshold: true, RawText: row.Raw.String}
			if row.Raw.Value != nil {
				a.Raw = *row.Raw.Value
			}
			attrs = append(attrs, a)
		}
		applyATAAttributes(disk, attrs)
	}
}

func metric(id, name, value, status string) model.SmartMetric {
	return model.SmartMetric{ID: id, Name: name, Value: value, Status: status}
}

func statusIf(cond bool, sev string) string {
	if cond {
		return sev
	}
	return "ok"
}

func u64(v *uint64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprint(*v)
}

// nvmeHealth is the NVMe SMART/health log, whichever tool produced it.
type nvmeHealth struct {
	CriticalWarning  *uint64
	TemperatureC     *float64
	AvailableSpare   *float64
	SpareThreshold   *float64
	PercentUsed      *float64
	DataUnitsRead    *uint64 // 1 unit = 512,000 bytes
	DataUnitsWritten *uint64
	HostReads        *uint64
	HostWrites       *uint64
	ControllerBusy   *uint64 // minutes
	PowerCycles      *uint64
	PowerOnHours     *uint64
	UnsafeShutdowns  *uint64
	MediaErrors      *uint64
	ErrorLogEntries  *uint64
	WarningTempTime  *uint64 // minutes
	CriticalTempTime *uint64
	Sensors          []float64
}

func applyNVMeHealth(disk *model.HWStorage, h nvmeHealth) {
	if disk.Health == "" {
		disk.Health = "Healthy"
	}
	if h.TemperatureC != nil && *h.TemperatureC > 0 {
		disk.TemperatureC = h.TemperatureC
	}
	if h.PowerOnHours != nil {
		disk.PowerOnHours = h.PowerOnHours
	}
	if h.PercentUsed != nil {
		disk.WearPercent = h.PercentUsed
	}
	if h.MediaErrors != nil {
		disk.MediaErrors = *h.MediaErrors
	}
	nvmeCritical(disk, h.CriticalWarning, h.AvailableSpare, h.SpareThreshold)

	add := func(name, value, status string) {
		disk.SMART = append(disk.SMART, metric("", name, value, status))
	}
	if h.CriticalWarning != nil {
		add("Critical warning", fmt.Sprintf("0x%02x", *h.CriticalWarning), statusIf(*h.CriticalWarning != 0, "critical"))
	}
	if h.TemperatureC != nil {
		temp := *h.TemperatureC
		st := "ok"
		switch {
		case temp >= 70:
			st = "critical"
		case temp >= 60:
			st = "warning"
		}
		add("Temperature", fmt.Sprintf("%.0f °C", temp), st)
	}
	for i, sensor := range h.Sensors {
		if sensor > 0 {
			add(fmt.Sprintf("Temperature sensor %d", i+1), fmt.Sprintf("%.0f °C", sensor), "ok")
		}
	}
	if h.AvailableSpare != nil {
		st := "ok"
		if h.SpareThreshold != nil && *h.AvailableSpare < *h.SpareThreshold {
			st = "warning"
		}
		add("Available spare", fmt.Sprintf("%.0f %%", *h.AvailableSpare), st)
	}
	if h.SpareThreshold != nil {
		add("Available spare threshold", fmt.Sprintf("%.0f %%", *h.SpareThreshold), "ok")
	}
	if h.PercentUsed != nil {
		st := "ok"
		switch {
		case *h.PercentUsed >= 90:
			st = "critical"
		case *h.PercentUsed >= 80:
			st = "warning"
		}
		add("Percentage used (wear)", fmt.Sprintf("%.0f %%", *h.PercentUsed), st)
	}
	if h.DataUnitsWritten != nil {
		add("Data written", model.FormatBytes(float64(*h.DataUnitsWritten)*512000), "ok")
	}
	if h.DataUnitsRead != nil {
		add("Data read", model.FormatBytes(float64(*h.DataUnitsRead)*512000), "ok")
	}
	if h.HostWrites != nil {
		add("Host write commands", u64(h.HostWrites), "ok")
	}
	if h.HostReads != nil {
		add("Host read commands", u64(h.HostReads), "ok")
	}
	if h.ControllerBusy != nil {
		add("Controller busy time", fmt.Sprintf("%d min", *h.ControllerBusy), "ok")
	}
	if h.PowerCycles != nil {
		add("Power cycles", u64(h.PowerCycles), "ok")
	}
	if h.PowerOnHours != nil {
		add("Power on hours", u64(h.PowerOnHours), "ok")
	}
	if h.UnsafeShutdowns != nil {
		add("Unsafe shutdowns", u64(h.UnsafeShutdowns), "ok")
	}
	if h.MediaErrors != nil {
		add("Media and data integrity errors", u64(h.MediaErrors), statusIf(*h.MediaErrors > 0, "warning"))
	}
	if h.ErrorLogEntries != nil {
		add("Error information log entries", u64(h.ErrorLogEntries), "ok")
	}
	if h.WarningTempTime != nil {
		add("Warning composite temperature time", fmt.Sprintf("%d min", *h.WarningTempTime), statusIf(*h.WarningTempTime > 0, "warning"))
	}
	if h.CriticalTempTime != nil {
		add("Critical composite temperature time", fmt.Sprintf("%d min", *h.CriticalTempTime), statusIf(*h.CriticalTempTime > 0, "critical"))
	}
}

// nvmeCritical turns the NVMe critical-warning bitmask and spare capacity
// into a health verdict and readable issues.
func nvmeCritical(disk *model.HWStorage, critical *uint64, spare, threshold *float64) {
	if critical != nil && *critical != 0 {
		disk.Health = "Unhealthy"
		labels := []string{
			"spare capacity di bawah ambang", "suhu di luar batas", "reliabilitas media menurun",
			"media read-only", "backup memori volatil gagal", "persistent memory region",
		}
		for bit, label := range labels {
			if *critical&(1<<uint(bit)) != 0 {
				disk.Issues = append(disk.Issues, "NVMe critical warning: "+label)
			}
		}
	}
	if spare != nil && threshold != nil && *spare < *threshold {
		if disk.Health != "Unhealthy" {
			disk.Health = "Warning"
		}
		disk.Issues = append(disk.Issues, fmt.Sprintf("spare %.0f%% di bawah ambang %.0f%%", *spare, *threshold))
	}
}

// applyNVMeCLI reads `nvme smart-log -o json`; used when smartctl is absent.
func applyNVMeCLI(disk *model.HWStorage, data []byte) error {
	var log struct {
		CriticalWarning *uint64  `json:"critical_warning"`
		Temperature     *float64 `json:"temperature"`
		AvailSpare      *float64 `json:"avail_spare"`
		SpareThresh     *float64 `json:"spare_thresh"`
		PercentUsed     *float64 `json:"percent_used"`
		DataUnitsRead   *uint64  `json:"data_units_read"`
		DataUnitsWrite  *uint64  `json:"data_units_written"`
		HostReads       *uint64  `json:"host_read_commands"`
		HostWrites      *uint64  `json:"host_write_commands"`
		ControllerBusy  *uint64  `json:"controller_busy_time"`
		PowerCycles     *uint64  `json:"power_cycles"`
		PowerOnHours    *uint64  `json:"power_on_hours"`
		UnsafeShutdowns *uint64  `json:"unsafe_shutdowns"`
		MediaErrors     *uint64  `json:"media_errors"`
		ErrorLogEntries *uint64  `json:"num_err_log_entries"`
		WarningTempTime *uint64  `json:"warning_temp_time"`
		CriticalTemp    *uint64  `json:"critical_comp_time"`
		Sensor1         *float64 `json:"temperature_sensor_1"`
		Sensor2         *float64 `json:"temperature_sensor_2"`
		Sensor3         *float64 `json:"temperature_sensor_3"`
		Sensor4         *float64 `json:"temperature_sensor_4"`
	}
	if err := json.Unmarshal(data, &log); err != nil {
		return err
	}
	if log.CriticalWarning == nil {
		return fmt.Errorf("format smart-log tidak dikenal")
	}
	// nvme-cli reports Kelvin; guard against builds that print Celsius.
	celsius := func(v *float64) *float64 {
		if v == nil || *v <= 0 {
			return nil
		}
		c := *v
		if c > 200 {
			c -= 273.15
		}
		c = math.Round(c*10) / 10
		return &c
	}
	h := nvmeHealth{
		CriticalWarning: log.CriticalWarning, TemperatureC: celsius(log.Temperature),
		AvailableSpare: log.AvailSpare, SpareThreshold: log.SpareThresh, PercentUsed: log.PercentUsed,
		DataUnitsRead: log.DataUnitsRead, DataUnitsWritten: log.DataUnitsWrite,
		HostReads: log.HostReads, HostWrites: log.HostWrites, ControllerBusy: log.ControllerBusy,
		PowerCycles: log.PowerCycles, PowerOnHours: log.PowerOnHours,
		UnsafeShutdowns: log.UnsafeShutdowns, MediaErrors: log.MediaErrors,
		ErrorLogEntries: log.ErrorLogEntries, WarningTempTime: log.WarningTempTime,
		CriticalTempTime: log.CriticalTemp,
	}
	for _, s := range []*float64{log.Sensor1, log.Sensor2, log.Sensor3, log.Sensor4} {
		if c := celsius(s); c != nil {
			h.Sensors = append(h.Sensors, *c)
		}
	}
	disk.Health = "Healthy"
	applyNVMeHealth(disk, h)
	disk.HealthNote = ""
	return nil
}

func megaraidDisk(slot string, payload *smartPayload) model.HWStorage {
	disk := model.HWStorage{
		Name:   slot,
		Model:  payload.ModelName,
		Serial: payload.SerialNumber,
		Bus:    "RAID member",
	}
	disk.ID = strings.NewReplacer(" ", "_", "/", "_").Replace(strings.TrimSpace(payload.ModelName + "_" + payload.SerialNumber))
	if disk.ID == "_" || disk.ID == "" {
		disk.ID = slot
	}
	if payload.UserCapacity != nil {
		disk.Size = payload.UserCapacity.Bytes
	}
	if payload.RotationRate != nil {
		if *payload.RotationRate == 0 {
			disk.Media = "SSD"
		} else {
			disk.Media = "HDD"
		}
	}
	applySMART(&disk, payload)
	return disk
}
