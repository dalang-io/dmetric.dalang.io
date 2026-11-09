package hardware

import (
	"encoding/json"
	"fmt"
	"strings"

	"dmetric.dalang.io/internal/model"
)

// flexList accepts either a JSON array or a single object, since Windows
// PowerShell serializes one-element arrays both ways depending on the
// pipeline.
type flexList[T any] []T

func (l *flexList[T]) UnmarshalJSON(data []byte) error {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 || string(data) == "null" {
		*l = nil
		return nil
	}
	if data[0] == '[' {
		var items []T
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		*l = items
		return nil
	}
	var one T
	if err := json.Unmarshal(data, &one); err != nil {
		return err
	}
	*l = []T{one}
	return nil
}

type winPayload struct {
	Board      *winBoard           `json:"Board"`
	Bios       *winBios            `json:"Bios"`
	System     *winSystem          `json:"System"`
	CPU        flexList[winCPU]    `json:"CPU"`
	CpuTemp    *float64            `json:"CpuTemp"`
	Memory     flexList[winMemory] `json:"Memory"`
	GPUs       flexList[winGPU]    `json:"GPUs"`
	NPUs       flexList[winNPU]    `json:"NPUs"`
	Fans       flexList[winFan]    `json:"Fans"`
	PSUs       flexList[winPSU]    `json:"PSUs"`
	Disks      flexList[winDisk]   `json:"Disks"`
	WheaErrors *int                `json:"WheaErrors"`
}

type winBoard struct {
	Manufacturer string `json:"Manufacturer"`
	Product      string `json:"Product"`
	SerialNumber string `json:"SerialNumber"`
}

type winBios struct {
	Manufacturer      string `json:"Manufacturer"`
	SMBIOSBIOSVersion string `json:"SMBIOSBIOSVersion"`
	ReleaseDate       string `json:"ReleaseDate"`
}

type winSystem struct {
	Manufacturer        string  `json:"Manufacturer"`
	Model               string  `json:"Model"`
	TotalPhysicalMemory *uint64 `json:"TotalPhysicalMemory"`
}

type winCPU struct {
	Name                      string `json:"Name"`
	ProcessorId               string `json:"ProcessorId"`
	NumberOfCores             *int   `json:"NumberOfCores"`
	NumberOfLogicalProcessors *int   `json:"NumberOfLogicalProcessors"`
	MaxClockSpeed             *int   `json:"MaxClockSpeed"`
	CurrentClockSpeed         *int   `json:"CurrentClockSpeed"`
}

type winGPU struct {
	Name   string  `json:"Name"`
	Vendor string  `json:"Vendor"`
	Memory *uint64 `json:"Memory"`
	Driver string  `json:"Driver"`
	Status string  `json:"Status"`
}

type winNPU struct {
	Name   string `json:"Name"`
	Status string `json:"Status"`
}

type winFan struct {
	Name         string  `json:"Name"`
	Status       string  `json:"Status"`
	DesiredSpeed *uint64 `json:"DesiredSpeed"`
}

type winPSU struct {
	Name        string  `json:"Name"`
	Status      string  `json:"Status"`
	ActivePower *uint64 `json:"ActivePower"`
}

type winMemory struct {
	DeviceLocator        string  `json:"DeviceLocator"`
	BankLabel            string  `json:"BankLabel"`
	Capacity             *uint64 `json:"Capacity"`
	Speed                *int    `json:"Speed"`
	ConfiguredClockSpeed *int    `json:"ConfiguredClockSpeed"`
	Manufacturer         string  `json:"Manufacturer"`
	PartNumber           string  `json:"PartNumber"`
	SerialNumber         string  `json:"SerialNumber"`
}

type winDisk struct {
	FriendlyName      string   `json:"FriendlyName"`
	MediaType         string   `json:"MediaType"`
	BusType           string   `json:"BusType"`
	Size              *uint64  `json:"Size"`
	HealthStatus      string   `json:"HealthStatus"`
	OperationalStatus string   `json:"OperationalStatus"`
	SerialNumber      string   `json:"SerialNumber"`
	Temperature       *float64 `json:"Temperature"`
	Wear              *float64 `json:"Wear"`
	PowerOnHours      *uint64  `json:"PowerOnHours"`
	ReadErrorsTotal   *uint64  `json:"ReadErrorsTotal"`
	WriteErrorsTotal  *uint64  `json:"WriteErrorsTotal"`
}

// parseWindowsPayload maps the JSON produced by the PowerShell script. It
// does not finalize the health verdicts, so the caller can fill fallbacks
// first.
func parseWindowsPayload(data []byte) (model.Hardware, error) {
	var payload winPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return model.Hardware{}, err
	}
	hw := empty("Windows CIM/PowerShell")

	if payload.Board != nil {
		hw.System.Vendor = clean(payload.Board.Manufacturer)
		hw.System.Product = clean(payload.Board.Product)
		hw.System.Serial = clean(payload.Board.SerialNumber)
	}
	if payload.Bios != nil {
		hw.System.BIOSVersion = clean(payload.Bios.SMBIOSBIOSVersion)
		hw.System.BIOSDate = clean(payload.Bios.ReleaseDate)
	}
	if payload.System != nil {
		if hw.System.Vendor == "" {
			hw.System.Vendor = clean(payload.System.Manufacturer)
		}
		if hw.System.Product == "" {
			hw.System.Product = clean(payload.System.Model)
		}
	}

	for _, cpu := range payload.CPU {
		if hw.CPU.Model == "" {
			hw.CPU.Model = clean(cpu.Name)
		}
		if hw.CPU.Serial == "" {
			hw.CPU.Serial = clean(cpu.ProcessorId)
		}
		if cpu.NumberOfCores != nil {
			hw.CPU.Cores += *cpu.NumberOfCores
		}
		if cpu.NumberOfLogicalProcessors != nil {
			hw.CPU.Threads += *cpu.NumberOfLogicalProcessors
		}
		if cpu.MaxClockSpeed != nil && *cpu.MaxClockSpeed > hw.CPU.MaxMHz {
			hw.CPU.MaxMHz = *cpu.MaxClockSpeed
		}
	}

	for _, module := range payload.Memory {
		item := model.HWModule{
			Locator:      clean(module.DeviceLocator),
			Bank:         clean(module.BankLabel),
			Manufacturer: clean(module.Manufacturer),
			PartNumber:   clean(module.PartNumber),
			Serial:       clean(module.SerialNumber),
		}
		if module.Capacity != nil {
			item.Size = *module.Capacity
			hw.Memory.Total += *module.Capacity
		}
		if module.Speed != nil && *module.Speed > 0 {
			item.SpeedMHz = *module.Speed
		} else if module.ConfiguredClockSpeed != nil {
			item.SpeedMHz = *module.ConfiguredClockSpeed
		}
		hw.Memory.Modules = append(hw.Memory.Modules, item)
	}
	if hw.Memory.Total == 0 && payload.System != nil && payload.System.TotalPhysicalMemory != nil {
		hw.Memory.Total = *payload.System.TotalPhysicalMemory
	}
	if payload.CpuTemp != nil && *payload.CpuTemp > 0 {
		hw.CPU.TemperatureC = payload.CpuTemp
		hw.CPU.TemperatureSource = "ACPI thermal zone"
	}

	for _, gpu := range payload.GPUs {
		item := model.HWAccelerator{
			Name:   clean(gpu.Name),
			Vendor: clean(gpu.Vendor),
			Type:   "GPU",
			Driver: clean(gpu.Driver),
			Status: clean(gpu.Status),
		}
		if gpu.Memory != nil {
			item.Memory = *gpu.Memory
		}
		hw.Accelerators = append(hw.Accelerators, item)
	}
	for _, npu := range payload.NPUs {
		hw.Accelerators = append(hw.Accelerators, model.HWAccelerator{
			Name:   clean(npu.Name),
			Type:   "NPU",
			Status: clean(npu.Status),
		})
	}
	for _, fan := range payload.Fans {
		item := model.HWFan{
			Name:   clean(fan.Name),
			Status: clean(fan.Status),
			Source: "Win32_Fan",
		}
		if fan.DesiredSpeed != nil && *fan.DesiredSpeed > 0 {
			item.Detail = fmt.Sprintf("desired %.0f RPM", float64(*fan.DesiredSpeed))
		}
		hw.Fans = append(hw.Fans, item)
	}
	for _, psu := range payload.PSUs {
		item := model.HWPSU{
			Name:   clean(psu.Name),
			Status: clean(psu.Status),
			Source: "Win32_PowerSupply",
		}
		if psu.ActivePower != nil {
			item.Detail = fmt.Sprintf("ActivePower WMI: %d", *psu.ActivePower)
		}
		hw.PSUs = append(hw.PSUs, item)
	}

	for i, disk := range payload.Disks {
		name := clean(disk.FriendlyName)
		if name == "" {
			name = fmt.Sprintf("disk %d", i+1)
		}
		entry := model.HWStorage{
			Name:              name,
			Model:             name,
			Serial:            clean(disk.SerialNumber),
			Media:             clean(disk.MediaType),
			Bus:               clean(disk.BusType),
			Health:            clean(disk.HealthStatus),
			OperationalStatus: clean(disk.OperationalStatus),
		}
		if disk.Size != nil {
			entry.Size = *disk.Size
		}
		if disk.Temperature != nil && *disk.Temperature > 0 {
			entry.TemperatureC = disk.Temperature
		}
		if disk.Wear != nil && *disk.Wear > 0 {
			entry.WearPercent = disk.Wear
		}
		if disk.PowerOnHours != nil && *disk.PowerOnHours > 0 {
			entry.PowerOnHours = disk.PowerOnHours
		}
		if disk.ReadErrorsTotal != nil {
			entry.ReadErrors = *disk.ReadErrorsTotal
		}
		if disk.WriteErrorsTotal != nil {
			entry.WriteErrors = *disk.WriteErrorsTotal
		}
		hw.Storage = append(hw.Storage, entry)
	}

	hw.System.WHEASupported = true
	if payload.WheaErrors != nil {
		hw.System.WHEAErrors = *payload.WheaErrors
	}
	return hw, nil
}

func clean(s string) string {
	return strings.TrimSpace(strings.Trim(s, "\x00"))
}
