package hardware

import (
	"testing"

	"dmetric.dalang.io/internal/model"
)

// smartPage builds a valid 512-byte SMART READ DATA page (checksum included).
func smartPage(attrs map[int][2]uint64) []byte { // id -> {current, raw}
	page := make([]byte, 512)
	i := 0
	for id, v := range attrs {
		e := page[2+i*12:]
		e[0] = byte(id)
		e[3] = byte(v[0])
		e[4] = byte(v[0])
		for j := 0; j < 6; j++ {
			e[5+j] = byte(v[1] >> (8 * uint(j)))
		}
		i++
	}
	var sum byte
	for _, b := range page[:511] {
		sum += b
	}
	page[511] = -sum
	return page
}

func senseWith(mid, high byte) []byte {
	s := make([]byte, 32)
	s[0] = 0x72
	s[7] = 14 // additional sense length
	s[8], s[9] = 0x09, 0x0c
	s[8+9], s[8+11] = mid, high
	return s
}

func TestParseATASmartPage(t *testing.T) {
	page := smartPage(map[int][2]uint64{
		9:   {99, 22000},
		194: {70, 0x0000_0030_0000_001f}, // temp 31 in low byte, min/max above
		177: {97, 0},                     // Samsung wear-leveling: 97% left
		5:   {100, 12},
		199: {100, 0},
	})
	attrs, err := parseATASmartData(page)
	if err != nil {
		t.Fatal(err)
	}
	d := model.HWStorage{}
	applyATASmart(&d, attrs, smartVerdictPassed)
	assessStorage(&d)
	if d.Health != "Healthy" {
		t.Fatalf("health = %q", d.Health)
	}
	if d.PowerOnHours == nil || *d.PowerOnHours != 22000 {
		t.Fatalf("poh = %v", d.PowerOnHours)
	}
	if d.TemperatureC == nil || *d.TemperatureC != 31 {
		t.Fatalf("temp = %v (min/max bytes must be ignored)", d.TemperatureC)
	}
	if d.WearPercent == nil || *d.WearPercent != 3 {
		t.Fatalf("wear = %v, want 3", d.WearPercent)
	}
	if d.Status != StatusWarning || len(d.Issues) != 1 { // 12 reallocated sectors
		t.Fatalf("status=%q issues=%v", d.Status, d.Issues)
	}
}

func TestParseATASmartPageRejectsBadData(t *testing.T) {
	if _, err := parseATASmartData(make([]byte, 100)); err == nil {
		t.Fatal("short page must fail")
	}
	page := smartPage(map[int][2]uint64{9: {99, 1}})
	page[3] ^= 0xff // corrupt
	if _, err := parseATASmartData(page); err == nil {
		t.Fatal("bad checksum must fail instead of reporting garbage health")
	}
}

func TestSMARTReturnStatusVerdict(t *testing.T) {
	if parseSMARTReturnStatus(senseWith(0x4f, 0xc2)) != smartVerdictPassed {
		t.Fatal("4F/C2 must be passed")
	}
	if parseSMARTReturnStatus(senseWith(0xf4, 0x2c)) != smartVerdictFailed {
		t.Fatal("F4/2C must be failed")
	}
	if parseSMARTReturnStatus(senseWith(0x00, 0x00)) != smartVerdictUnknown {
		t.Fatal("other registers must be unknown, never OK")
	}
	if parseSMARTReturnStatus(nil) != smartVerdictUnknown || parseSMARTReturnStatus(make([]byte, 32)) != smartVerdictUnknown {
		t.Fatal("empty/fixed-format sense must be unknown")
	}
}

func TestFailedVerdictIsCritical(t *testing.T) {
	d := model.HWStorage{}
	applyATASmart(&d, nil, smartVerdictFailed)
	assessStorage(&d)
	if d.Status != StatusCritical || d.Health != "Unhealthy" {
		t.Fatalf("status=%q health=%q", d.Status, d.Health)
	}
}

func TestThresholdsAndMetricList(t *testing.T) {
	page := smartPage(map[int][2]uint64{9: {99, 22000}, 5: {100, 0}, 177: {40, 0}, 241: {99, 123456}})
	attrs, err := parseATASmartData(page)
	if err != nil {
		t.Fatal(err)
	}
	// Threshold page: reuse the layout, byte 1 = threshold.
	th := make([]byte, 512)
	i := 0
	for _, id := range []int{9, 5, 177, 241} {
		th[2+i*12], th[2+i*12+1] = byte(id), 50
		i++
	}
	var sum byte
	for _, b := range th[:511] {
		sum += b
	}
	th[511] = -sum
	thresholds, err := parseATAThresholds(th)
	if err != nil || thresholds[177] != 50 {
		t.Fatalf("thresholds = %v err=%v", thresholds, err)
	}
	mergeThresholds(attrs, thresholds)

	d := model.HWStorage{}
	applyATASmart(&d, attrs, smartVerdictPassed)
	if len(d.SMART) != 4 {
		t.Fatalf("expected all 4 attributes listed, got %d: %+v", len(d.SMART), d.SMART)
	}
	byID := map[string]model.SmartMetric{}
	for _, m := range d.SMART {
		byID[m.ID] = m
	}
	if byID["177"].Status != "critical" || byID["177"].Name != "Wear_Leveling_Count" {
		t.Fatalf("177 below threshold must be critical: %+v", byID["177"])
	}
	if byID["9"].Value != "22000 jam" || byID["9"].Threshold == nil || *byID["9"].Threshold != 50 {
		t.Fatalf("poh metric: %+v", byID["9"])
	}
	if byID["241"].Value != "123456" || byID["5"].Status != "ok" {
		t.Fatalf("raw/status wrong: %+v %+v", byID["241"], byID["5"])
	}
}

func TestNVMeMetricListFromRealFixture(t *testing.T) {
	d := model.HWStorage{}
	if err := applyNVMeCLI(&d, []byte(nvmeCLIFixture)); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, m := range d.SMART {
		names[m.Name] = m.Value
	}
	for _, want := range []string{"Temperature", "Available spare", "Percentage used (wear)", "Power cycles",
		"Power on hours", "Unsafe shutdowns", "Media and data integrity errors", "Critical warning"} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing SMART metric %q (have %v)", want, names)
		}
	}
	if names["Data written"] == "" || names["Power cycles"] != "130" || names["Temperature sensor 2"] != "34 °C" {
		t.Fatalf("data written / power cycles / sensor 2 wrong: %v", names)
	}
	if names["Unsafe shutdowns"] != "82" || names["Power on hours"] != "191" {
		t.Fatalf("values wrong: %v", names)
	}
}

func TestVendorSpecificCountersAreNotFlaggedByRaw(t *testing.T) {
	// A cheap SSD reporting a large raw value on attribute 181 is not a failure.
	attrs := []ataAttribute{
		{ID: 181, Current: 100, Worst: 100, Raw: 756053, HasThreshold: true, Threshold: 0},
		{ID: 197, Current: 100, Worst: 100, Raw: 2, HasThreshold: true, Threshold: 0},
	}
	got := ataMetrics(attrs)
	if got[0].Status != "ok" || got[1].Status != "warning" {
		t.Fatalf("181=%q (want ok), 197=%q (want warning)", got[0].Status, got[1].Status)
	}
}
