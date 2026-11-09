//go:build darwin

package hardware

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"dmetric.dalang.io/internal/model"
)

func collect(ctx context.Context) model.Hardware {
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "system_profiler", "-json",
		"SPHardwareDataType", "SPMemoryDataType", "SPStorageDataType",
		"SPNVMeDataType", "SPSerialATADataType", "SPDisplaysDataType")
	out, err := cmd.Output()
	if err != nil {
		return empty("macOS system_profiler", "gagal membaca hardware: "+err.Error())
	}
	var payload map[string][]map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		return empty("macOS system_profiler", "output hardware tidak valid: "+err.Error())
	}
	hw := empty("macOS system_profiler")

	if items := payload["SPHardwareDataType"]; len(items) > 0 {
		item := items[0]
		hw.System.Vendor = "Apple"
		hw.System.Product = firstText(item, "machine_name", "machine_model", "model_name")
		hw.System.Serial = text(item, "serial_number")
		hw.CPU.Model = firstText(item, "chip_type", "processor_name")
		hw.Memory.Total = parseHumanSize(text(item, "physical_memory"))
	}
	hw.CPU.Threads = runtime.NumCPU()
	hw.CPU.Cores = hw.CPU.Threads

	for _, item := range payload["SPMemoryDataType"] {
		sizeText := firstText(item, "SPMemoryDataType", "dimm_size", "size")
		size := parseHumanSize(sizeText)
		if size == 0 {
			continue
		}
		hw.Memory.Modules = append(hw.Memory.Modules, model.HWModule{
			Locator:      firstText(item, "dimm_slot", "slot"),
			Manufacturer: text(item, "dimm_manufacturer"),
			PartNumber:   text(item, "dimm_part_number"),
			Serial:       text(item, "dimm_serial_number"),
			Size:         size,
			SpeedMHz:     firstInt(text(item, "dimm_speed")),
		})
		hw.Memory.Total = maxU64(hw.Memory.Total, size)
	}
	if len(payload["SPMemoryDataType"]) > 0 {
		hw.Notes = append(hw.Notes, "ECC tidak tersedia di macOS; gunakan log sistem untuk memory error")
	}

	seen := map[string]int{}
	for _, item := range payload["SPStorageDataType"] {
		drive, _ := item["physical_drive"].(map[string]any)
		if drive == nil {
			continue
		}
		device := firstText(drive, "device_name", "media_name")
		if device == "" || strings.EqualFold(device, "Disk Image") ||
			strings.EqualFold(text(drive, "protocol"), "Disk Image") {
			continue
		}
		size := uint64(number(item, "size_in_bytes"))
		index, ok := seen[device]
		if !ok {
			entry := model.HWStorage{
				Name:   device,
				Model:  device,
				Media:  strings.ToUpper(text(drive, "medium_type")),
				Bus:    text(drive, "protocol"),
				Health: text(drive, "smart_status"),
				Size:   size,
			}
			hw.Storage = append(hw.Storage, entry)
			seen[device] = len(hw.Storage) - 1
			continue
		}
		if size > hw.Storage[index].Size {
			hw.Storage[index].Size = size
		}
	}
	// SPStorageDataType does not expose the drive serial; the NVMe and SATA
	// device families do.
	for _, controller := range payload["SPNVMeDataType"] {
		items, _ := controller["_items"].([]any)
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			applyMacDrive(&hw, item, "NVMe")
		}
	}
	for _, item := range payload["SPSerialATADataType"] {
		applyMacDrive(&hw, item, "SATA")
		if items, ok := item["_items"].([]any); ok {
			for _, raw := range items {
				if child, ok := raw.(map[string]any); ok {
					applyMacDrive(&hw, child, "SATA")
				}
			}
		}
	}
	for _, item := range payload["SPDisplaysDataType"] {
		name := firstText(item, "sppci_model", "_name")
		if name == "" {
			continue
		}
		vendor := firstText(item, "spdisplays_vendor", "spdisplays_chipset")
		vendor = strings.TrimPrefix(vendor, "sppci_vendor_")
		gpu := model.HWAccelerator{
			Name:   name,
			Vendor: vendor,
			Type:   "GPU",
			Status: "OK",
		}
		if vram := parseHumanSize(text(item, "spdisplays_vram")); vram > 0 {
			gpu.Memory = vram
		}
		hw.Accelerators = append(hw.Accelerators, gpu)
	}
	if len(hw.Storage) > 0 {
		hw.Notes = append(hw.Notes, "macOS hanya memberi status SMART ringkas; install smartctl untuk atribut lengkap")
	}
	if len(hw.Fans) == 0 {
		hw.Notes = append(hw.Notes, "Sensor fan tidak diekspos macOS")
	}
	if len(hw.PSUs) == 0 {
		hw.Notes = append(hw.Notes, "Status PSU tidak diekspos macOS")
	}
	hasNPU := false
	for _, accelerator := range hw.Accelerators {
		if accelerator.Type == "NPU" {
			hasNPU = true
		}
	}
	if !hasNPU {
		hw.Notes = append(hw.Notes, "Neural Engine/NPU tidak diekspos system_profiler")
	}

	finalize(&hw)
	return hw
}

func applyMacDrive(hw *model.Hardware, item map[string]any, defaultBus string) {
	name := firstText(item, "device_model", "_name", "device_name")
	if name == "" {
		return
	}
	serial := firstText(item, "device_serial", "serial_number")
	size := uint64(number(item, "size_in_bytes"))
	smart := text(item, "smart_status")

	for i := range hw.Storage {
		if hw.Storage[i].Name == name || hw.Storage[i].Model == name {
			if serial != "" {
				hw.Storage[i].Serial = serial
			}
			if hw.Storage[i].Health == "" && smart != "" {
				hw.Storage[i].Health = smart
			}
			if hw.Storage[i].Size == 0 && size > 0 {
				hw.Storage[i].Size = size
			}
			if hw.Storage[i].Bus == "" {
				hw.Storage[i].Bus = defaultBus
			}
			return
		}
	}
	if size == 0 {
		return
	}
	hw.Storage = append(hw.Storage, model.HWStorage{
		Name:   name,
		Model:  name,
		Serial: serial,
		Media:  "SSD",
		Bus:    defaultBus,
		Size:   size,
		Health: smart,
	})
}

func text(item map[string]any, key string) string {
	if value, ok := item[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func firstText(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := text(item, key); value != "" {
			return value
		}
	}
	return ""
}

func number(item map[string]any, key string) float64 {
	switch value := item[key].(type) {
	case float64:
		return value
	case string:
		n, _ := strconv.ParseFloat(value, 64)
		return n
	}
	return 0
}

func parseHumanSize(value string) uint64 {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	multiplier := float64(1)
	if len(fields) > 1 {
		switch strings.ToUpper(strings.Trim(fields[1], "()")) {
		case "KB":
			multiplier = 1 << 10
		case "MB":
			multiplier = 1 << 20
		case "GB":
			multiplier = 1 << 30
		case "TB":
			multiplier = 1 << 40
		}
	}
	return uint64(n * multiplier)
}

func maxU64(a, b uint64) uint64 {
	if b > a {
		return b
	}
	return a
}
