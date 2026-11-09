package hardware

import (
	"encoding/json"
	"testing"

	"dmetric.dalang.io/internal/model"
)

// Captured from `nvme smart-log /dev/nvme0n1 -o json` (nvme-cli 2.16) on a
// production hypervisor. Temperature is Kelvin.
const nvmeCLIFixture = `{"critical_warning":0,"temperature":320,"avail_spare":100,"spare_thresh":1,
"percent_used":0,"endurance_grp_critical_warning_summary":0,"data_units_read":2006912,
"data_units_written":5886277,"host_read_commands":26171674,"host_write_commands":187068194,
"controller_busy_time":202,"power_cycles":130,"power_on_hours":191,"unsafe_shutdowns":82,
"media_errors":0,"num_err_log_entries":0,"warning_temp_time":0,"critical_comp_time":0,
"temperature_sensor_1":320,"temperature_sensor_2":307}`

func TestNVMeCLIHealthy(t *testing.T) {
	d := model.HWStorage{Name: "nvme0n1", Bus: "NVMe", HealthNote: "x"}
	if err := applyNVMeCLI(&d, []byte(nvmeCLIFixture)); err != nil {
		t.Fatal(err)
	}
	assessStorage(&d)
	if d.Health != "Healthy" || d.Status != StatusOK || d.HealthNote != "" {
		t.Fatalf("health=%q status=%q note=%q issues=%v", d.Health, d.Status, d.HealthNote, d.Issues)
	}
	if d.TemperatureC == nil || int(*d.TemperatureC+0.5) != 47 {
		t.Fatalf("temperature = %v, want ~47C", d.TemperatureC)
	}
	if d.PowerOnHours == nil || *d.PowerOnHours != 191 || d.WearPercent == nil || *d.WearPercent != 0 {
		t.Fatalf("poh/wear wrong: %v %v", d.PowerOnHours, d.WearPercent)
	}
}

func TestNVMeCLICriticalWarning(t *testing.T) {
	d := model.HWStorage{}
	err := applyNVMeCLI(&d, []byte(`{"critical_warning":8,"temperature":300,"avail_spare":100,"spare_thresh":10,"media_errors":3}`))
	if err != nil {
		t.Fatal(err)
	}
	assessStorage(&d)
	if d.Health != "Unhealthy" || d.Status != StatusCritical || d.MediaErrors != 3 {
		t.Fatalf("health=%q status=%q media=%d issues=%v", d.Health, d.Status, d.MediaErrors, d.Issues)
	}
}

func TestNVMeCLILowSpareAndCelsius(t *testing.T) {
	d := model.HWStorage{}
	if err := applyNVMeCLI(&d, []byte(`{"critical_warning":0,"temperature":41,"avail_spare":5,"spare_thresh":10}`)); err != nil {
		t.Fatal(err)
	}
	assessStorage(&d)
	if d.Status != StatusWarning || d.TemperatureC == nil || *d.TemperatureC != 41 {
		t.Fatalf("status=%q temp=%v issues=%v", d.Status, d.TemperatureC, d.Issues)
	}
	if err := applyNVMeCLI(&model.HWStorage{}, []byte(`{"foo":1}`)); err == nil {
		t.Fatal("unknown format must be an error, not a silent OK")
	}
}

func TestSmartctlNVMeNestedLog(t *testing.T) {
	var p smartPayload
	raw := `{"serial_number":"S1","smart_status":{"passed":true},
	"temperature":{"current":44},"power_on_time":{"hours":1200},
	"nvme_smart_health_information_log":{"critical_warning":0,"available_spare":100,
	"available_spare_threshold":10,"percentage_used":12,"media_errors":2}}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	d := model.HWStorage{}
	applySMART(&d, &p)
	assessStorage(&d)
	if d.WearPercent == nil || *d.WearPercent != 12 || d.MediaErrors != 2 || d.Health != "Healthy" {
		t.Fatalf("nested nvme log not applied: wear=%v media=%d health=%q", d.WearPercent, d.MediaErrors, d.Health)
	}
	if d.Status != StatusWarning { // media errors > 0
		t.Fatalf("status = %q, issues=%v", d.Status, d.Issues)
	}
}

func TestSmartctlATAAttributes(t *testing.T) {
	var p smartPayload
	raw := `{"smart_status":{"passed":true},"ata_smart_attributes":{"table":[
	{"id":5,"raw":{"value":8}},{"id":197,"raw":{"value":0}},{"id":199,"raw":{"value":4}}]}}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	d := model.HWStorage{}
	applySMART(&d, &p)
	assessStorage(&d)
	if len(d.Issues) != 2 || d.Status != StatusWarning {
		t.Fatalf("status=%q issues=%v", d.Status, d.Issues)
	}
}

func TestIsRAIDVirtualDisk(t *testing.T) {
	for _, m := range []string{"PERC H330 Mini", "MegaRAID SAS 9361", "LOGICAL VOLUME", "Smart Array P440ar"} {
		if !isRAIDVirtualDisk(m) {
			t.Errorf("%q should be a RAID virtual disk", m)
		}
	}
	for _, m := range []string{"SAMSUNG MZ7KM960", "TEAM TM8FPW004T", "KYO", ""} {
		if isRAIDVirtualDisk(m) {
			t.Errorf("%q must not be a RAID virtual disk", m)
		}
	}
}

func TestMegaraidMemberDisk(t *testing.T) {
	rate := 0
	p := &smartPayload{ModelName: "SEAGATE ST300", SerialNumber: "ZZ1", RotationRate: &rate}
	d := megaraidDisk("megaraid,3", p)
	if d.ID != "SEAGATE_ST300_ZZ1" || d.Media != "SSD" || d.Bus != "RAID member" {
		t.Fatalf("unexpected: %+v", d)
	}
}
