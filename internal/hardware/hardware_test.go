package hardware

import (
	"strings"
	"testing"

	"dmetric.dalang.io/internal/model"
)

func TestAssessStorageHealth(t *testing.T) {
	warn := 85.0
	temp := 72.0
	disk := model.HWStorage{
		Name:              "D:",
		Model:             "HPE LOGICAL VOLUME",
		Health:            "Warning",
		OperationalStatus: "Degraded",
		WearPercent:       &warn,
		TemperatureC:      &temp,
		ReadErrors:        2,
	}
	assessStorage(&disk)
	if disk.Status != StatusCritical {
		t.Fatalf("status = %q, issues=%v", disk.Status, disk.Issues)
	}
	if len(disk.Issues) < 3 {
		t.Fatalf("expected health, operational, temperature and error issues: %v", disk.Issues)
	}
}

func TestFinalizeHardwareStatus(t *testing.T) {
	hw := empty("test")
	hw.System.Vendor = "HPE"
	hw.System.Product = "ProLiant"
	hw.CPU.Model = "Xeon"
	hw.Memory.Total = 32 << 30
	hw.Memory.ECCUncorrectable = 1
	hw.Storage = []model.HWStorage{{Name: "C:", Health: "Healthy"}}
	finalize(&hw)
	if hw.Status != StatusCritical {
		t.Fatalf("overall = %q, want critical; issues=%v", hw.Status, hw.Issues)
	}
	if len(hw.Issues) == 0 || !strings.Contains(strings.Join(hw.Issues, " "), "ECC") {
		t.Fatalf("ECC issue missing: %v", hw.Issues)
	}
}

func TestParseWindowsPayload(t *testing.T) {
	fixture := []byte(`{
	  "Board":{"Manufacturer":"HPE","Product":"ProLiant DL380 Gen10","SerialNumber":"CZ123"},
	  "Bios":{"Manufacturer":"HPE","SMBIOSBIOSVersion":"U30 v2.80","ReleaseDate":"2023-01-01"},
	  "System":{"Manufacturer":"HPE","Model":"ProLiant DL380 Gen10","TotalPhysicalMemory":34359738368},
	  "CPU":[{"Name":"Intel(R) Xeon(R) Gold 6248","ProcessorId":"BFEBFBFF00050657","NumberOfCores":20,"NumberOfLogicalProcessors":40,"MaxClockSpeed":2500}],
	  "CpuTemp":48.5,
	  "Memory":[
	    {"DeviceLocator":"PROC 1 DIMM 1","BankLabel":"A","Capacity":17179869184,"Speed":null,"ConfiguredClockSpeed":2933,"Manufacturer":"Samsung","PartNumber":"M393","SerialNumber":"S1"},
	    {"DeviceLocator":"PROC 1 DIMM 2","BankLabel":"B","Capacity":17179869184,"Speed":2933,"Manufacturer":"Samsung","PartNumber":"M393","SerialNumber":"S2"}
	  ],
	  "GPUs":[{"Name":"NVIDIA Tesla T4","Vendor":"NVIDIA","Memory":17179869184,"Driver":"31.0.15.3699","Status":"OK"}],
	  "NPUs":[{"Name":"Intel(R) AI Boost","Status":"OK"}],
	  "Fans":[{"Name":"System Fan 1","Status":"OK","DesiredSpeed":4200}],
	  "PSUs":[{"Name":"Power Supply 1","Status":"OK","ActivePower":1800}],
	  "Disks":[
	    {"FriendlyName":"HPE LOGICAL VOLUME","MediaType":"HDD","BusType":"RAID","Size":1999999999999,"HealthStatus":"Healthy","OperationalStatus":"OK","Temperature":35,"Wear":0,"PowerOnHours":1200},
	    {"FriendlyName":"SSD OS","MediaType":"SSD","BusType":"RAID","Size":479000000000,"HealthStatus":"Warning","OperationalStatus":"Degraded","Temperature":61,"Wear":86,"PowerOnHours":9000,"ReadErrorsTotal":3}
	  ],
	  "WheaErrors":2
	}`)
	hw, err := parseWindowsPayload(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if hw.System.Vendor != "HPE" || hw.System.BIOSVersion != "U30 v2.80" {
		t.Fatalf("system parsing: %+v", hw.System)
	}
	if hw.CPU.Cores != 20 || hw.CPU.Threads != 40 || hw.CPU.MaxMHz != 2500 {
		t.Fatalf("CPU parsing: %+v", hw.CPU)
	}
	if hw.CPU.Serial != "BFEBFBFF00050657" || hw.CPU.TemperatureC == nil || *hw.CPU.TemperatureC != 48.5 {
		t.Fatalf("CPU identity/temperature parsing: %+v", hw.CPU)
	}
	if len(hw.Accelerators) != 2 || hw.Accelerators[1].Type != "NPU" || hw.Accelerators[0].Memory != 16<<30 {
		t.Fatalf("accelerator parsing: %+v", hw.Accelerators)
	}
	if len(hw.Fans) != 1 || hw.Fans[0].Detail == "" || len(hw.PSUs) != 1 {
		t.Fatalf("fan/PSU parsing: fans=%+v psus=%+v", hw.Fans, hw.PSUs)
	}
	if hw.Memory.Total != 32<<30 || len(hw.Memory.Modules) != 2 {
		t.Fatalf("memory parsing: %+v", hw.Memory)
	}
	if hw.Memory.Modules[0].Serial != "S1" || hw.Memory.Modules[0].SpeedMHz != 2933 || hw.Memory.Modules[0].Locator != "PROC 1 DIMM 1" {
		t.Fatalf("module identity parsing: %+v", hw.Memory.Modules[0])
	}
	if len(hw.Storage) != 2 || hw.Storage[0].Name != "HPE LOGICAL VOLUME" {
		t.Fatalf("storage parsing: %+v", hw.Storage)
	}
	if hw.Storage[1].WearPercent == nil || *hw.Storage[1].WearPercent != 86 {
		t.Fatalf("wear parsing: %+v", hw.Storage[1])
	}
	finalize(&hw)
	if hw.Status != StatusCritical {
		t.Fatalf("overall = %q; issues=%v", hw.Status, hw.Issues)
	}
}

func TestAssessFanAndPSU(t *testing.T) {
	rpm := 0.0
	min := 1200.0
	fan := model.HWFan{Name: "FAN1", RPM: &rpm, MinRPM: &min, Status: "ok"}
	assessFan(&fan)
	if fan.Status != StatusCritical {
		t.Fatalf("fan status = %q", fan.Status)
	}
	psu := model.HWPSU{Name: "PSU1", Status: "cr", Detail: "AC lost"}
	assessPSU(&psu)
	if psu.Status != StatusCritical || len(psu.Issues) == 0 {
		t.Fatalf("PSU status = %q issues=%v", psu.Status, psu.Issues)
	}

	for _, status := range []string{"nc", "lnc", "unc"} {
		item := model.HWPSU{Name: "PSU", Status: status}
		assessPSU(&item)
		if item.Status != StatusWarning {
			t.Errorf("PSU %q mapped to %q, want warning", status, item.Status)
		}
	}
	for _, status := range []string{"nr", "lnr", "unr", "lost"} {
		item := model.HWPSU{Name: "PSU", Status: status}
		assessPSU(&item)
		if item.Status != StatusCritical {
			t.Errorf("PSU %q mapped to %q, want critical", status, item.Status)
		}
	}
	unknown := model.HWPSU{Name: "PSU", Status: "0x01"}
	assessPSU(&unknown)
	if unknown.Status != StatusUnknown {
		t.Fatalf("unknown PSU status mapped to %q", unknown.Status)
	}

	stopped := 0.0
	fan = model.HWFan{Name: "FAN2", RPM: &stopped, Status: "ok"}
	assessFan(&fan)
	if fan.Status != StatusCritical || len(fan.Issues) == 0 {
		t.Fatalf("stopped fan without min: status=%q issues=%v", fan.Status, fan.Issues)
	}
	warned := model.HWFan{Name: "FAN3", Status: "nc"}
	assessFan(&warned)
	if warned.Status != StatusWarning || len(warned.Issues) == 0 {
		t.Fatalf("warning fan: status=%q issues=%v", warned.Status, warned.Issues)
	}
	if statusFromText("Predictive Failure") != StatusWarning {
		t.Fatal("predictive failure should be warning")
	}
	if statusFromText("No Redundancy") != StatusCritical {
		t.Fatal("no redundancy should be critical")
	}
}
