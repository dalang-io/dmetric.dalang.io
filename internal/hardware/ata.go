package hardware

import (
	"fmt"

	"dmetric.dalang.io/internal/model"
)

// Native ATA SMART support: DMetric reads SMART itself (SG_IO ATA
// pass-through on Linux) so health works without smartmontools. This file
// holds the OS-independent parsing; the ioctl lives in sgio_linux.go.

// SMART RETURN STATUS verdicts, from the LBA mid/high registers the drive
// returns (ATA8-ACS): 4F/C2 = threshold not exceeded, F4/2C = exceeded.
const (
	smartVerdictUnknown = iota
	smartVerdictPassed
	smartVerdictFailed
)

// ATA SMART command set (ATA8-ACS), shared by every transport.
const (
	ataCmdSmart      = 0xb0
	smartReadData    = 0xd0
	smartReadThresh  = 0xd1
	smartReturnState = 0xda
	smartLBAMid      = 0x4f
	smartLBAHigh     = 0xc2
)

// ataTransport sends one SMART sub-command (the features register) to a
// drive. With wantData it returns the 512-byte data page; otherwise it
// returns the LBA mid/high registers the drive reported back. SG_IO (Linux)
// and IOCTL_ATA_PASS_THROUGH (Windows) both implement it.
type ataTransport func(feature uint8, wantData bool) (page []byte, mid, high uint8, err error)

// readATAWith runs the full SMART read: attributes, thresholds, verdict.
func readATAWith(send ataTransport) ([]ataAttribute, int, error) {
	page, _, _, err := send(smartReadData, true)
	if err != nil {
		return nil, 0, err
	}
	attrs, err := parseATASmartData(page)
	if err != nil {
		return nil, 0, err
	}
	// Thresholds are optional: losing them only loses the threshold column.
	if thresh, _, _, err := send(smartReadThresh, true); err == nil {
		if th, perr := parseATAThresholds(thresh); perr == nil {
			mergeThresholds(attrs, th)
		}
	}
	verdict := smartVerdictUnknown
	if _, mid, high, err := send(smartReturnState, false); err == nil {
		verdict = verdictFromRegisters(mid, high)
	}
	return attrs, verdict, nil
}

// verdictFromRegisters decodes SMART RETURN STATUS: 4F/C2 means the
// threshold is not exceeded, F4/2C means it is. Anything else is unknown,
// never "passed".
func verdictFromRegisters(mid, high uint8) int {
	switch {
	case mid == 0x4f && high == 0xc2:
		return smartVerdictPassed
	case mid == 0xf4 && high == 0x2c:
		return smartVerdictFailed
	}
	return smartVerdictUnknown
}

type ataAttribute struct {
	ID           int
	Name         string // from smartctl; native reads use ataAttributeNames
	Current      uint8
	Worst        uint8
	Raw          uint64 // 48-bit raw value
	RawText      string // smartctl's formatted raw, when available
	Threshold    int
	HasThreshold bool
}

// Common ATA attribute names (smartmontools drivedb conventions).
var ataAttributeNames = map[int]string{
	1: "Raw_Read_Error_Rate", 2: "Throughput_Performance", 3: "Spin_Up_Time", 4: "Start_Stop_Count",
	5: "Reallocated_Sector_Ct", 7: "Seek_Error_Rate", 8: "Seek_Time_Performance", 9: "Power_On_Hours",
	10: "Spin_Retry_Count", 11: "Calibration_Retry_Count", 12: "Power_Cycle_Count",
	171: "Program_Fail_Count", 172: "Erase_Fail_Count", 173: "Wear_Leveling_Count",
	174: "Unexpected_Power_Loss_Ct", 175: "Program_Fail_Count_Chip", 176: "Erase_Fail_Count_Chip",
	177: "Wear_Leveling_Count", 178: "Used_Rsvd_Blk_Cnt_Chip", 179: "Used_Rsvd_Blk_Cnt_Tot",
	180: "Unused_Rsvd_Blk_Cnt_Tot", 181: "Program_Fail_Cnt_Total", 182: "Erase_Fail_Count_Total",
	183: "Runtime_Bad_Block", 184: "End-to-End_Error", 187: "Reported_Uncorrect",
	188: "Command_Timeout", 189: "High_Fly_Writes", 190: "Airflow_Temperature_Cel",
	191: "G-Sense_Error_Rate", 192: "Power-Off_Retract_Count", 193: "Load_Cycle_Count",
	194: "Temperature_Celsius", 195: "Hardware_ECC_Recovered", 196: "Reallocated_Event_Count",
	197: "Current_Pending_Sector", 198: "Offline_Uncorrectable", 199: "UDMA_CRC_Error_Count",
	200: "Multi_Zone_Error_Rate", 202: "Percent_Lifetime_Remain", 206: "Flying_Height",
	210: "Vibration_During_Write", 231: "SSD_Life_Left", 232: "Available_Reservd_Space",
	233: "Media_Wearout_Indicator", 234: "Thermal_Throttle_Status", 235: "POR_Recovery_Count",
	241: "Total_LBAs_Written", 242: "Total_LBAs_Read", 245: "Flash_Writes_GiB",
}

// ataCounterAttributes are error counters with a universal meaning, where
// any non-zero raw value is a finding. Vendor-specific counters (program/erase
// fail totals, runtime bad blocks...) are judged only by their threshold,
// because their raw value means different things on different drives.
var ataCounterAttributes = map[int]bool{
	5: true, 187: true, 196: true, 197: true, 198: true, 199: true,
}

// parseATASmartData decodes the 512-byte SMART READ DATA page: 30 attribute
// slots of 12 bytes starting at offset 2.
func parseATASmartData(page []byte) ([]ataAttribute, error) {
	if err := checkSMARTPage(page); err != nil {
		return nil, err
	}
	attrs := []ataAttribute{}
	for i := 0; i < 30; i++ {
		entry := page[2+i*12 : 2+(i+1)*12]
		if entry[0] == 0 {
			continue
		}
		var raw uint64
		for j := 5; j >= 0; j-- {
			raw = raw<<8 | uint64(entry[5+j])
		}
		attrs = append(attrs, ataAttribute{ID: int(entry[0]), Current: entry[3], Worst: entry[4], Raw: raw})
	}
	return attrs, nil
}

// parseATAThresholds decodes SMART READ THRESHOLDS (same layout; byte 1 of
// each slot is the threshold).
func parseATAThresholds(page []byte) (map[int]int, error) {
	if err := checkSMARTPage(page); err != nil {
		return nil, err
	}
	out := map[int]int{}
	for i := 0; i < 30; i++ {
		entry := page[2+i*12 : 2+(i+1)*12]
		if entry[0] != 0 {
			out[int(entry[0])] = int(entry[1])
		}
	}
	return out, nil
}

// checkSMARTPage verifies length and checksum so garbage from a misbehaving
// HBA is never reported as drive health.
func checkSMARTPage(page []byte) error {
	if len(page) < 512 {
		return fmt.Errorf("halaman SMART %d byte, butuh 512", len(page))
	}
	var sum byte
	for _, b := range page[:512] {
		sum += b
	}
	if sum != 0 {
		return fmt.Errorf("checksum halaman SMART salah")
	}
	return nil
}

// mergeThresholds attaches thresholds to attributes.
func mergeThresholds(attrs []ataAttribute, thresholds map[int]int) {
	for i := range attrs {
		if t, ok := thresholds[attrs[i].ID]; ok {
			attrs[i].Threshold = t
			attrs[i].HasThreshold = true
		}
	}
}

// senseRegisters reads the ATA Status Return descriptor from the SCSI sense
// data of a SMART RETURN STATUS pass-through (descriptor format 0x72).
func senseRegisters(sense []byte) (mid, high uint8, ok bool) {
	if len(sense) < 22 || (sense[0]&0x7f) != 0x72 {
		return 0, 0, false
	}
	// Descriptors start at byte 8; find the ATA Status Return (code 0x09).
	for off := 8; off+1 < len(sense); {
		code, length := sense[off], int(sense[off+1])
		if code == 0x09 && off+13 < len(sense) {
			return sense[off+9], sense[off+11], true
		}
		off += 2 + length
	}
	return 0, 0, false
}

// parseSMARTReturnStatus is the verdict from SCSI sense data.
func parseSMARTReturnStatus(sense []byte) int {
	mid, high, ok := senseRegisters(sense)
	if !ok {
		return smartVerdictUnknown
	}
	return verdictFromRegisters(mid, high)
}

// applyATASmart fills the disk from decoded attributes and the verdict.
func applyATASmart(disk *model.HWStorage, attrs []ataAttribute, verdict int) {
	switch verdict {
	case smartVerdictPassed:
		disk.Health = "Healthy"
	case smartVerdictFailed:
		disk.Health = "Unhealthy"
		disk.Issues = append(disk.Issues, "SMART: ambang atribut terlampaui (drive memprediksi kegagalan)")
	}
	applyATAAttributes(disk, attrs)
}

// applyATAAttributes derives the summary fields and the full SMART metric
// list from ATA attributes (native reader or smartctl).
func applyATAAttributes(disk *model.HWStorage, attrs []ataAttribute) {
	byID := map[int]ataAttribute{}
	for _, a := range attrs {
		byID[a.ID] = a
	}
	if a, ok := byID[9]; ok && disk.PowerOnHours == nil {
		hours := a.Raw & 0xffffffff
		disk.PowerOnHours = &hours
	}
	// Temperature: the low byte of the raw value (higher bytes hold min/max).
	if disk.TemperatureC == nil {
		for _, id := range []int{194, 190} {
			if a, ok := byID[id]; ok {
				if t := float64(a.Raw & 0xff); t > 0 && t < 120 {
					disk.TemperatureC = &t
					break
				}
			}
		}
	}
	// SSD wear: normalized value counts down from 100 on these attributes.
	if disk.WearPercent == nil {
		for _, id := range []int{177, 233, 231} {
			if a, ok := byID[id]; ok && a.Current > 0 && a.Current <= 100 {
				wear := float64(100 - int(a.Current))
				disk.WearPercent = &wear
				break
			}
		}
	}
	for _, spec := range []struct {
		id    int
		label string
	}{
		{5, "%d reallocated sector"},
		{197, "%d pending sector"},
		{198, "%d offline-uncorrectable sector"},
		{199, "%d UDMA CRC error (cek kabel/port)"},
	} {
		if a, ok := byID[spec.id]; ok && a.Raw > 0 {
			disk.Issues = append(disk.Issues, fmt.Sprintf(spec.label, a.Raw))
		}
	}
	disk.SMART = append(disk.SMART, ataMetrics(attrs)...)
}

func ataMetrics(attrs []ataAttribute) []model.SmartMetric {
	out := make([]model.SmartMetric, 0, len(attrs))
	for _, a := range attrs {
		name := a.Name
		if name == "" {
			name = ataAttributeNames[a.ID]
		}
		if name == "" {
			name = fmt.Sprintf("Attribute_%d", a.ID)
		}
		cur, worst := int(a.Current), int(a.Worst)
		m := model.SmartMetric{
			ID: fmt.Sprint(a.ID), Name: name, Value: ataRawText(a),
			Normalized: &cur, Worst: &worst, Status: "ok",
		}
		if a.HasThreshold {
			th := a.Threshold
			m.Threshold = &th
			// A normalized value at or below its threshold is the drive's own
			// failure signal (threshold 0 means "never fails").
			if th > 0 && cur > 0 && cur <= th {
				m.Status = "critical"
			}
		}
		if m.Status == "ok" && ataCounterAttributes[a.ID] && a.Raw > 0 {
			m.Status = "warning"
		}
		out = append(out, m)
	}
	return out
}

func ataRawText(a ataAttribute) string {
	switch a.ID {
	case 194, 190:
		return fmt.Sprintf("%d °C", a.Raw&0xff)
	case 9:
		return fmt.Sprintf("%d jam", a.Raw&0xffffffff)
	}
	if a.RawText != "" {
		return a.RawText
	}
	return fmt.Sprint(a.Raw)
}
