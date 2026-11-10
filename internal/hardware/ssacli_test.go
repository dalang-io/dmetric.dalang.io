package hardware

import (
	"strings"
	"testing"

	"dmetric.dalang.io/internal/model"
)

// These fixtures follow the layout of `ssacli ctrl all show config detail`
// documented by HPE (ssacli 5.x, Smart Array P440ar). They are REPRESENTATIVE,
// not captured from a real server: compare against real output before relying
// on field spellings (notably SSD wear keys, which vary between versions).
const ssacliHealthy = `
Smart Array P440ar in Slot 0 (Embedded)
   Bus Interface: PCI
   Slot: 0
   Serial Number: PDNNK0ARH9X1A2
   Controller Status: OK
   Hardware Revision: B
   Firmware Version: 7.00
   Rebuild Priority: Medium
   Surface Scan Delay: 3 secs
   Cache Board Present: True
   Cache Status: OK
   Cache Ratio: 10% Read / 90% Write
   Total Cache Size: 2.0
   Battery/Capacitor Count: 1
   Battery/Capacitor Status: OK
   Driver Name: HpSAMD
   Driver Version: 63.1.2.0

   Array: A
      Interface Type: Solid State SATA
      Unused Space: 0  MB (0.0%)
      Status: OK
      Array Type: Data

      Logical Drive: 1
         Size: 447.1 GB
         Fault Tolerance: 1
         Strip Size: 256 KB
         Status: OK
         Caching:  Enabled
         Unique Identifier: 600508B1001C3A1B2C3D4E5F60718293
         Logical Drive Label: 05A1B2C3
         Drive Type: Data
         LD Acceleration Method: Controller Cache

      physicaldrive 1I:1:1
         Port: 1I
         Box: 1
         Bay: 1
         Status: OK
         Drive Type: Data Drive
         Interface Type: Solid State SATA
         Size: 480 GB
         Drive exposed to OS: False
         Logical/Physical Block Size: 512/512
         Rotational Speed: 0
         Firmware Revision: HPG1
         Serial Number: SSDSER0001
         WWID: 5000000000000A0A
         Model: ATA     EXAMPLE480
         Current Temperature (C): 31
         Maximum Temperature (C): 38
         Usage remaining: 99.50%
         Power On Hours: 1011

   Array: B
      Interface Type: SAS
      Status: OK
      Array Type: Data

      Logical Drive: 2
         Size: 558.7 GB
         Fault Tolerance: 5
         Strip Size: 256 KB
         Status: OK
         Caching:  Enabled
         Unique Identifier: 600508B1001C0000000000000000BEEF
         Drive Type: Data

      physicaldrive 1I:1:3
         Port: 1I
         Box: 1
         Bay: 3
         Status: OK
         Drive Type: Data Drive
         Interface Type: SAS
         Size: 300 GB
         Rotational Speed: 15000
         Serial Number: S0K0HDD3
         WWID: 5000C500A1B2C3D4
         Model: HP      EG0300FBVFL
         Current Temperature (C): 34
         Maximum Temperature (C): 45

   Unassigned
      physicaldrive 1I:1:8
         Port: 1I
         Box: 1
         Bay: 8
         Status: OK
         Drive Type: Unassigned Drive
         Interface Type: SAS
         Size: 300 GB
         Rotational Speed: 15000
         Serial Number: S0K0SPARE
         Model: HP      EG0300FBVFL
         Current Temperature (C): 33
`

const ssacliDegraded = `
Smart Array P440ar in Slot 0 (Embedded)
   Slot: 0
   Serial Number: PDNNK0ARH9X1A2
   Controller Status: OK
   Cache Status: Temporarily Disabled
   Battery/Capacitor Count: 1
   Battery/Capacitor Status: Failed

   Array: B
      Interface Type: SAS
      Status: Failed Drive Replacement Needed

      Logical Drive: 2
         Size: 558.7 GB
         Fault Tolerance: 5
         Status: Interim Recovery Mode
         Unique Identifier: 600508B1001C0000000000000000BEEF

      physicaldrive 1I:1:3
         Status: Failed
         Interface Type: SAS
         Size: 300 GB
         Serial Number: S0K0HDD3
         Model: HP      EG0300FBVFL
      physicaldrive 1I:1:4
         Status: Predictive Failure
         Interface Type: SAS
         Size: 300 GB
         Serial Number: S0K0HDD4
         Model: HP      EG0300FBVFL
`

func TestParseSSACLIStructure(t *testing.T) {
	ctrls := parseSSACLI(ssacliHealthy)
	if len(ctrls) != 1 {
		t.Fatalf("controllers = %d", len(ctrls))
	}
	c := ctrls[0]
	if c.Model != "Smart Array P440ar" || c.Slot != "0" || c.Fields["Cache Status"] != "OK" {
		t.Fatalf("controller: %+v", c)
	}
	if len(c.LDs) != 2 || len(c.PDs) != 3 {
		t.Fatalf("lds=%d pds=%d", len(c.LDs), len(c.PDs))
	}
	if c.LDs[1].Fields["Fault Tolerance"] != "5" || c.LDs[1].Array != "B" {
		t.Fatalf("ld2: %+v", c.LDs[1])
	}
	if c.PDs[2].Array != "" || c.PDs[2].Location != "1I:1:8" {
		t.Fatalf("unassigned drive not recognised: %+v", c.PDs[2])
	}
	if got := collapseSpaces(c.PDs[1].Fields["Model"]); got != "HP EG0300FBVFL" {
		t.Fatalf("model = %q", got)
	}
}

func TestParseSSACLIIgnoresErrors(t *testing.T) {
	if got := parseSSACLI("Error: No controllers detected.\n"); len(got) != 0 {
		t.Fatalf("expected no controllers, got %d", len(got))
	}
	if got := parseSSACLI(""); len(got) != 0 {
		t.Fatal("empty output")
	}
}

func hwWith(ids ...string) model.Hardware {
	hw := empty("test")
	for _, id := range ids {
		hw.Storage = append(hw.Storage, model.HWStorage{ID: id, Name: "HPE LOGICAL VOLUME", Bus: "RAID", Media: "HDD"})
	}
	return hw
}

func TestSSACLIHealthyMergesIntoWindowsDisks(t *testing.T) {
	// Windows reports each logical volume with its NAA UniqueId.
	hw := hwWith("naa.600508b1001c3a1b2c3d4e5f60718293", "naa.600508b1001c0000000000000000beef")
	applySSACLI(&hw, parseSSACLI(ssacliHealthy))
	finalize(&hw)

	byName := map[string]model.HWStorage{}
	for _, d := range hw.Storage {
		byName[d.Name] = d
	}
	ld1 := hw.Storage[0]
	if ld1.Media != "RAID 1" || ld1.Health != "Healthy" {
		t.Fatalf("LD1 not enriched: media=%q health=%q", ld1.Media, ld1.Health)
	}
	if hw.Storage[1].Media != "RAID 5" {
		t.Fatalf("LD2 media = %q", hw.Storage[1].Media)
	}
	if n := len(hw.Storage); n != 2+1+3 { // 2 volumes + controller + 3 drives
		t.Fatalf("storage rows = %d, want 6 (matched LDs must not be duplicated)", n)
	}
	ssd := byName["1I:1:1"]
	if ssd.ID != "naa.5000000000000a0a" || ssd.Media != "SSD" || ssd.Bus != "RAID member" {
		t.Fatalf("ssd: %+v", ssd)
	}
	if ssd.TemperatureC == nil || *ssd.TemperatureC != 31 || ssd.WearPercent == nil || *ssd.WearPercent != 0.5 ||
		ssd.PowerOnHours == nil || *ssd.PowerOnHours != 1011 {
		t.Fatalf("ssd metrics: temp=%v wear=%v poh=%v", ssd.TemperatureC, ssd.WearPercent, ssd.PowerOnHours)
	}
	hdd := byName["1I:1:3"]
	if hdd.Media != "HDD" || hdd.Size != 300_000_000_000 || hdd.Model != "HP EG0300FBVFL" {
		t.Fatalf("hdd: %+v", hdd)
	}
	ctrl := byName["Smart Array P440ar in Slot 0 (Embedded)"]
	if ctrl.Health != "Healthy" || ctrl.Status != StatusOK || len(ctrl.SMART) < 4 {
		t.Fatalf("controller: health=%q status=%q metrics=%d", ctrl.Health, ctrl.Status, len(ctrl.SMART))
	}
	if hw.Status != StatusOK {
		t.Fatalf("overall = %q, issues=%v", hw.Status, hw.Issues)
	}
}

func TestSSACLIDegradedIsCritical(t *testing.T) {
	hw := hwWith("naa.600508b1001c0000000000000000beef")
	applySSACLI(&hw, parseSSACLI(ssacliDegraded))
	finalize(&hw)
	byName := map[string]model.HWStorage{}
	for _, d := range hw.Storage {
		byName[d.Name] = d
	}
	if d := hw.Storage[0]; d.Status != StatusCritical || !strings.Contains(d.OperationalStatus, "Interim Recovery") {
		t.Fatalf("degraded LD: status=%q op=%q", d.Status, d.OperationalStatus)
	}
	if d := byName["1I:1:3"]; d.Status != StatusCritical {
		t.Fatalf("failed drive status = %q", d.Status)
	}
	if d := byName["1I:1:4"]; d.Status != StatusCritical {
		t.Fatalf("predictive-failure drive status = %q (must not look OK)", d.Status)
	}
	ctrl := byName["Smart Array P440ar in Slot 0 (Embedded)"]
	if ctrl.Status != StatusCritical || len(ctrl.Issues) != 2 {
		t.Fatalf("controller: status=%q issues=%v", ctrl.Status, ctrl.Issues)
	}
	if hw.Status != StatusCritical {
		t.Fatalf("overall status = %q, want critical", hw.Status)
	}
}

func TestSSACLIUnmatchedLogicalDriveGetsOwnRow(t *testing.T) {
	hw := hwWith() // the OS exposes nothing that matches
	applySSACLI(&hw, parseSSACLI(ssacliHealthy))
	found := 0
	for _, d := range hw.Storage {
		if strings.HasPrefix(d.Name, "Logical drive") {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("unmatched logical drives shown = %d, want 2", found)
	}
}

func TestSSACLISizeAndLevel(t *testing.T) {
	if parseSSASize("1.9 TB") != 1_900_000_000_000 || parseSSASize("300 GB") != 300_000_000_000 || parseSSASize("x") != 0 {
		t.Fatal("parseSSASize")
	}
	for in, want := range map[string]string{"1": "RAID 1", "5": "RAID 5", "1+0": "RAID 1+0", "1adm": "RAID 1 ADM", "": "RAID"} {
		if got := ssaRAIDLevel(in); got != want {
			t.Errorf("ssaRAIDLevel(%q) = %q, want %q", in, got, want)
		}
	}
	for status, wantHealth := range map[string]string{"OK": "Healthy", "Failed": "Unhealthy", "Predictive Failure": "Unhealthy",
		"Rebuilding": "Warning", "Recovering": "Warning", "Interim Recovery Mode": "Warning"} {
		if h, _ := ssaVerdict(status); h != wantHealth {
			t.Errorf("ssaVerdict(%q) health = %q, want %q", status, h, wantHealth)
		}
	}
}

func TestWindowsDiskID(t *testing.T) {
	cases := []struct{ unique, friendly, serial, want string }{
		{"600508B1001C3A1B2C3D4E5F60718293", "HPE LOGICAL VOLUME", "", "naa.600508b1001c3a1b2c3d4e5f60718293"},
		{"naa.5000C500A1B2C3D4", "EG0300", "S1", "naa.5000c500a1b2c3d4"},
		{"eui.00000000000000000A1B2C3D4E5F6071", "TEAM", "S2", "eui.00000000000000000a1b2c3d4e5f6071"},
		{"0000000000000000", "SSD 1TB", "SER9", "SSD_1TB_SER9"}, // placeholder id -> serial
		{"{1234-5678}", "WD Blue", "WX123", "WD_Blue_WX123"},    // not an NAA id
		{"", "Virtual", "", ""}, // nothing stable known
	}
	for _, c := range cases {
		if got := windowsDiskID(c.unique, c.friendly, c.serial); got != c.want {
			t.Errorf("windowsDiskID(%q,%q,%q) = %q, want %q", c.unique, c.friendly, c.serial, got, c.want)
		}
	}
	if windowsDevicePath("3") != `\\.\PhysicalDrive3` || windowsDevicePath("x") != "" || windowsDevicePath("") != "" {
		t.Fatal("windowsDevicePath")
	}
}

func TestDedupeStorageIDs(t *testing.T) {
	disks := []model.HWStorage{{ID: "a", Name: "x"}, {ID: "a", Name: "y"}, {ID: "a", Name: "a"}, {ID: "b", Name: "z"}}
	dedupeStorageIDs(disks)
	if disks[0].ID != "a" || disks[1].ID != "a [y]" || disks[2].ID != "a [#3]" || disks[3].ID != "b" {
		t.Fatalf("ids: %v %v %v %v", disks[0].ID, disks[1].ID, disks[2].ID, disks[3].ID)
	}
}

func TestReadATAWithTransport(t *testing.T) {
	page := smartPage(map[int][2]uint64{9: {99, 500}, 5: {100, 0}})
	th := make([]byte, 512)
	th[2], th[3] = 5, 10
	var sum byte
	for _, b := range th[:511] {
		sum += b
	}
	th[511] = -sum

	var order []uint8
	send := func(feature uint8, wantData bool) ([]byte, uint8, uint8, error) {
		order = append(order, feature)
		switch feature {
		case smartReadData:
			return page, 0, 0, nil
		case smartReadThresh:
			return th, 0, 0, nil
		case smartReturnState:
			return nil, 0xf4, 0x2c, nil // threshold exceeded
		}
		t.Fatalf("unexpected feature %#x", feature)
		return nil, 0, 0, nil
	}
	attrs, verdict, err := readATAWith(send)
	if err != nil {
		t.Fatal(err)
	}
	if verdict != smartVerdictFailed {
		t.Fatalf("verdict = %d, want failed", verdict)
	}
	if len(order) != 3 || order[0] != smartReadData {
		t.Fatalf("command order: %v", order)
	}
	var five *ataAttribute
	for i := range attrs {
		if attrs[i].ID == 5 {
			five = &attrs[i]
		}
	}
	if five == nil || !five.HasThreshold || five.Threshold != 10 {
		t.Fatalf("threshold not merged: %+v", five)
	}

	// A failing data read must surface as an error, not as a healthy disk.
	_, _, err = readATAWith(func(uint8, bool) ([]byte, uint8, uint8, error) {
		return nil, 0, 0, errTest
	})
	if err == nil {
		t.Fatal("transport error must propagate")
	}
}

type testErr string

func (e testErr) Error() string { return string(e) }

const errTest = testErr("boom")
