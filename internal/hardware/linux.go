//go:build linux

package hardware

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"

	"dmetric.dalang.io/internal/diskid"
	"dmetric.dalang.io/internal/model"
)

func collect(ctx context.Context) model.Hardware {
	hw := empty("Linux sysfs")
	hw.System.Vendor = sysfsString("/sys/class/dmi/id/sys_vendor")
	hw.System.Product = sysfsString("/sys/class/dmi/id/product_name")
	hw.System.Serial = sysfsString("/sys/class/dmi/id/product_serial")
	hw.System.BIOSVersion = sysfsString("/sys/class/dmi/id/bios_version")
	hw.System.BIOSDate = sysfsString("/sys/class/dmi/id/bios_date")
	if hw.System.Vendor == "" && hw.System.Product == "" {
		hw.Notes = append(hw.Notes, "DMI motherboard tidak terbaca (VM/container?)")
	}

	hw.CPU.Model = cpuModel()
	if hw.CPU.Model == "" {
		if infos, err := cpu.InfoWithContext(ctx); err == nil && len(infos) > 0 {
			hw.CPU.Model = infos[0].ModelName
		}
	}
	hw.CPU.Threads = runtime.NumCPU()
	hw.CPU.Cores = hw.CPU.Threads
	if cores, err := cpu.Counts(false); err == nil && cores > 0 {
		hw.CPU.Cores = cores
	}
	hw.CPU.TemperatureC = cpuTemperature()
	if hw.CPU.TemperatureC != nil {
		hw.CPU.TemperatureSource = "hwmon/thermal"
	}
	hw.CPU.Serial = linuxCPUSerial(ctx)

	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		hw.Memory.Total = vm.Total
	}
	hw.Memory.Modules = dimmModules(ctx, &hw.Notes)
	ce, ue, ok := edacCounters("/sys/devices/system/edac/mc")
	if ok {
		hw.Memory.ECCSupported = true
		hw.Memory.ECCCorrectable = ce
		hw.Memory.ECCUncorrectable = ue
	} else {
		hw.Notes = append(hw.Notes, "EDAC tidak tersedia; error ECC tidak dapat dipantau")
	}

	hw.Fans = hwmonFans()
	hw.Accelerators = lspciAccelerators(ctx, &hw.Notes)
	ipmiFans, ipmiPSUs := collectIPMI(ctx, &hw.Notes)
	hw.Fans = append(hw.Fans, ipmiFans...)
	hw.PSUs = append(hw.PSUs, ipmiPSUs...)
	if len(hw.Fans) == 0 {
		hw.Notes = append(hw.Notes, "Sensor fan tidak tersedia (hwmon/IPMI)")
	}
	if len(hw.PSUs) == 0 {
		hw.Notes = append(hw.Notes, "Status PSU tidak tersedia (butuh ipmitool/iLO)")
	}
	hw.Storage = linuxStorage(ctx, &hw.Notes)
	finalize(&hw)
	return hw
}

func sysfsString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "model name") {
			if _, value, ok := strings.Cut(line, ":"); ok {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func linuxCPUSerial(ctx context.Context) string {
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if key, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(key), "Serial") {
				if serial := strings.TrimSpace(value); serial != "" {
					return serial
				}
			}
		}
	}
	path, err := exec.LookPath("dmidecode")
	if err != nil {
		return ""
	}
	runCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, path, "-t", "processor").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "Serial Number") {
			continue
		}
		serial := strings.TrimSpace(value)
		switch strings.ToLower(serial) {
		case "", "not specified", "unknown", "none", "not applicable":
			continue
		}
		return serial
	}
	return ""
}

func hwmonFans() []model.HWFan {
	fans := []model.HWFan{}
	dirs, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	for _, dir := range dirs {
		chip := sysfsString(filepath.Join(dir, "name"))
		inputs, _ := filepath.Glob(filepath.Join(dir, "fan*_input"))
		for _, input := range inputs {
			base := strings.TrimSuffix(filepath.Base(input), "_input")
			rpmValue, err := strconv.ParseFloat(sysfsString(input), 64)
			if err != nil {
				continue
			}
			name := sysfsString(filepath.Join(dir, base+"_label"))
			if name == "" {
				name = chip + " " + base
			}
			fan := model.HWFan{Name: name, RPM: &rpmValue, Source: "hwmon"}
			if minValue, err := strconv.ParseFloat(sysfsString(filepath.Join(dir, base+"_min")), 64); err == nil && minValue > 0 {
				fan.MinRPM = &minValue
			}
			fans = append(fans, fan)
		}
	}
	return fans
}

func lspciAccelerators(ctx context.Context, notes *[]string) []model.HWAccelerator {
	path, err := exec.LookPath("lspci")
	if err != nil {
		*notes = append(*notes, "lspci tidak ditemukan; GPU/NPU tidak terdeteksi")
		return []model.HWAccelerator{}
	}
	runCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, path, "-D").Output()
	if err != nil {
		*notes = append(*notes, "lspci gagal: "+shortError(err))
		return []model.HWAccelerator{}
	}
	accelerators := []model.HWAccelerator{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		slot, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		class, name, ok := strings.Cut(rest, ": ")
		if !ok {
			continue
		}
		lowerClass := strings.ToLower(class)
		kind := ""
		switch {
		case strings.Contains(lowerClass, "vga compatible controller"),
			strings.Contains(lowerClass, "3d controller"),
			strings.Contains(lowerClass, "display controller"):
			kind = "GPU"
		case strings.Contains(lowerClass, "processing accelerator"):
			kind = "NPU"
		default:
			continue
		}
		driver := ""
		if link, err := filepath.EvalSymlinks(filepath.Join("/sys/bus/pci/devices", slot, "driver")); err == nil {
			driver = filepath.Base(link)
		}
		accelerators = append(accelerators, model.HWAccelerator{
			Name:   strings.TrimSpace(name),
			Vendor: pciVendor(name),
			Type:   kind,
			Driver: driver,
			Status: "OK",
		})
	}
	return accelerators
}

func pciVendor(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "nvidia"):
		return "NVIDIA"
	case strings.Contains(lower, "intel"):
		return "Intel"
	case strings.Contains(lower, "advanced micro devices"), strings.Contains(lower, "amd"):
		return "AMD"
	case strings.Contains(lower, "apple"):
		return "Apple"
	}
	return ""
}

func cpuTemperature() *float64 {
	names := map[string]bool{"coretemp": true, "k10temp": true, "zenpower": true, "cpu_thermal": true}
	var best float64
	found := false
	dirs, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	for _, dir := range dirs {
		name := strings.ToLower(sysfsString(filepath.Join(dir, "name")))
		if !names[name] {
			continue
		}
		inputs, _ := filepath.Glob(filepath.Join(dir, "temp*_input"))
		for _, input := range inputs {
			raw, err := strconv.ParseFloat(sysfsString(input), 64)
			if err == nil && raw/1000 > best {
				best = raw / 1000
				found = true
			}
		}
	}
	if !found {
		zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
		for _, zone := range zones {
			raw, err := strconv.ParseFloat(sysfsString(zone), 64)
			if err == nil && raw/1000 > best {
				best = raw / 1000
				found = true
			}
		}
	}
	if !found || best <= 0 {
		return nil
	}
	return &best
}

func edacCounters(base string) (correctable, uncorrectable uint64, found bool) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0, 0, false
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "mc") {
			continue
		}
		found = true
		ce, _ := strconv.ParseUint(sysfsString(filepath.Join(base, entry.Name(), "ce_count")), 10, 64)
		ue, _ := strconv.ParseUint(sysfsString(filepath.Join(base, entry.Name(), "ue_count")), 10, 64)
		correctable += ce
		uncorrectable += ue
	}
	return correctable, uncorrectable, found
}

func dimmModules(ctx context.Context, notes *[]string) []model.HWModule {
	path, err := exec.LookPath("dmidecode")
	if err != nil {
		*notes = append(*notes, "dmidecode tidak ditemukan; detail modul RAM tidak tersedia")
		return []model.HWModule{}
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, path, "-t", "memory").Output()
	if err != nil || len(out) == 0 {
		*notes = append(*notes, "detail DIMM butuh dmidecode sebagai root")
		return []model.HWModule{}
	}
	return parseDMIMemory(string(out))
}

func parseDMIMemory(output string) []model.HWModule {
	modules := []model.HWModule{}
	var current model.HWModule
	inDevice := false
	flush := func() {
		if inDevice && current.Size > 0 {
			modules = append(modules, current)
		}
		current = model.HWModule{}
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Memory Device") {
			flush()
			inDevice = true
			continue
		}
		if !inDevice || !strings.Contains(line, ":") {
			continue
		}
		key, value, _ := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "size":
			if !strings.Contains(strings.ToLower(value), "no module") {
				current.Size = parseMemorySize(value)
			}
		case "locator":
			current.Locator = value
		case "bank locator":
			current.Bank = value
		case "speed", "configured memory speed":
			if speed := firstInt(value); speed > 0 {
				current.SpeedMHz = speed
			}
		case "manufacturer":
			current.Manufacturer = value
		case "part number":
			current.PartNumber = value
		case "serial number":
			current.Serial = value
		}
	}
	flush()
	return modules
}

func parseMemorySize(value string) uint64 {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0
	}
	number, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	multiplier := uint64(1 << 20)
	if len(fields) > 1 {
		switch strings.ToUpper(fields[1]) {
		case "GB":
			multiplier = 1 << 30
		case "MB":
			multiplier = 1 << 20
		}
	}
	return uint64(number) * multiplier
}

// linuxStorage lists physical disks only: Ceph RBD, loop, LVM/md and other
// virtual devices are skipped, and every disk is keyed by its WWN / by-id
// alias because sda/sdb are assigned in probe order and change on reboot.
func linuxStorage(ctx context.Context, notes *[]string) []model.HWStorage {
	return linuxStorageAt(ctx, notes, diskid.DefaultSysBlock, diskid.DefaultByIDDir)
}

func linuxStorageAt(ctx context.Context, notes *[]string, sysBlock, byIDDir string) []model.HWStorage {
	entries, err := os.ReadDir(sysBlock)
	if err != nil {
		return []model.HWStorage{}
	}
	names := []string{}
	for _, entry := range entries {
		if diskid.Physical(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	ids := diskid.Load(byIDDir).Resolve(sysBlock, names)

	smartPath, smartErr := exec.LookPath("smartctl")
	nvmePath, _ := exec.LookPath("nvme")
	if smartErr != nil {
		*notes = append(*notes, "smartctl tidak terpasang; SMART SATA dibaca langsung (SG_IO), NVMe via nvme-cli")
	}
	smartPermissionNote := false
	raidSeen := false
	out := []model.HWStorage{}
	for _, name := range names {
		base := filepath.Join(sysBlock, name)
		disk := model.HWStorage{
			ID:     ids[name],
			Name:   name,
			Model:  sysfsString(filepath.Join(base, "device", "model")),
			Serial: sysfsString(filepath.Join(base, "device", "serial")),
			Bus:    "SATA/SAS",
		}
		if strings.HasPrefix(name, "nvme") {
			disk.Bus = "NVMe"
		}
		if sectors, err := strconv.ParseUint(sysfsString(filepath.Join(base, "size")), 10, 64); err == nil {
			disk.Size = sectors * 512
		}
		switch sysfsString(filepath.Join(base, "queue", "rotational")) {
		case "1":
			disk.Media = "HDD"
		case "0":
			disk.Media = "SSD"
		}
		if isRAIDVirtualDisk(disk.Model) {
			// The OS only sees the controller's logical volume; media type and
			// SMART of the member drives are hidden behind it.
			disk.Bus = "RAID"
			disk.Media = "RAID virtual"
			raidSeen = true
			disk.HealthNote = "Virtual disk RAID: kesehatan drive fisik tidak terlihat dari OS"
			out = append(out, disk)
			continue
		}
		switch {
		case smartPath != "":
			if payload, err := readSMART(ctx, smartPath, "/dev/"+name); err == nil {
				applySMART(&disk, payload)
			} else if !smartPermissionNote {
				smartPermissionNote = true
				*notes = append(*notes, "SMART gagal dibaca (butuh root): "+shortError(err))
			}
		case disk.Bus == "NVMe" && nvmePath != "":
			if data, err := runNVMeSmartLog(ctx, nvmePath, "/dev/"+name); err == nil {
				if perr := applyNVMeCLI(&disk, data); perr != nil {
					disk.HealthNote = "nvme smart-log tidak terbaca: " + shortError(perr)
				}
			} else {
				disk.HealthNote = "nvme smart-log gagal: " + shortError(err)
			}
		case disk.Bus == "NVMe":
			disk.HealthNote = "Install smartmontools atau nvme-cli untuk membaca health NVMe"
		}
		if disk.Health == "" && disk.Bus == "SATA/SAS" {
			// No smartctl (or it returned nothing): read ATA SMART in-process.
			if err := nativeATAHealth(&disk, "/dev/"+name); err != nil {
				disk.HealthNote = "SMART tidak terbaca: " + shortError(err) + " (disk SAS/SCSI atau butuh root; smartmontools mendukung lebih banyak)"
			}
		}
		out = append(out, disk)
	}
	if raidSeen && smartPath != "" {
		out = append(out, megaraidDrives(ctx, smartPath, notes)...)
	} else if raidSeen {
		*notes = append(*notes, "RAID controller terdeteksi; drive fisik di belakangnya butuh smartctl (-d megaraid,N) atau storcli/iLO")
	}
	return out
}

func readSMART(ctx context.Context, path, device string, extra ...string) (*smartPayload, error) {
	runCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	args := append([]string{"-j", "-H", "-i", "-A"}, extra...)
	args = append(args, device)
	out, err := exec.CommandContext(runCtx, path, args...).Output()
	if len(out) == 0 {
		if err == nil {
			err = fmt.Errorf("tidak ada output")
		}
		return nil, err
	}
	var payload smartPayload
	if jsonErr := json.Unmarshal(out, &payload); jsonErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, jsonErr
	}
	return &payload, nil
}

func runNVMeSmartLog(ctx context.Context, path, device string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, path, "smart-log", device, "-o", "json").Output()
	if len(out) == 0 && err == nil {
		err = fmt.Errorf("tidak ada output")
	}
	if len(out) == 0 {
		return nil, err
	}
	return out, nil
}

// megaraidDrives reads the physical drives behind an LSI/Dell PERC
// controller through smartctl's megaraid pass-through.
func megaraidDrives(ctx context.Context, smartPath string, notes *[]string) []model.HWStorage {
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, smartPath, "--scan-open", "-j").Output()
	if len(out) == 0 {
		if err != nil {
			*notes = append(*notes, "scan drive RAID gagal: "+shortError(err))
		}
		return nil
	}
	var scan struct {
		Devices []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"devices"`
	}
	if json.Unmarshal(out, &scan) != nil {
		return nil
	}
	drives := []model.HWStorage{}
	for _, dev := range scan.Devices {
		if !strings.HasPrefix(dev.Type, "megaraid,") {
			continue
		}
		payload, err := readSMART(ctx, smartPath, dev.Name, "-d", dev.Type)
		if err != nil {
			continue
		}
		drives = append(drives, megaraidDisk(dev.Type, payload))
	}
	if len(drives) == 0 {
		*notes = append(*notes, "drive fisik di belakang RAID tidak terbaca smartctl (cek mode controller / storcli)")
	}
	return drives
}

func shortError(err error) string {
	text := err.Error()
	if len(text) > 100 {
		text = text[:100]
	}
	return text
}
