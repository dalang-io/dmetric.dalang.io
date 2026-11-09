//go:build windows

package hardware

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"

	"dmetric.dalang.io/internal/model"
)

func collect(ctx context.Context) model.Hardware {
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := runPowerShell(runCtx, windowsHardwareScript)
	if err != nil {
		return empty("Windows CIM/PowerShell", "gagal membaca hardware: "+err.Error())
	}
	hw, err := parseWindowsPayload(out)
	if err != nil {
		return empty("Windows CIM/PowerShell", "output hardware tidak valid: "+err.Error())
	}

	// Fallbacks when CIM returned less than expected.
	if hw.CPU.Model == "" {
		if infos, err := cpu.InfoWithContext(ctx); err == nil && len(infos) > 0 {
			hw.CPU.Model = infos[0].ModelName
		}
	}
	if hw.CPU.Threads == 0 {
		hw.CPU.Threads = runtime.NumCPU()
	}
	if hw.CPU.Cores == 0 {
		hw.CPU.Cores = hw.CPU.Threads
	}
	if hw.Memory.Total == 0 {
		if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
			hw.Memory.Total = vm.Total
		}
	}
	ipmiFans, ipmiPSUs := collectIPMI(ctx, &hw.Notes)
	hw.Fans = append(hw.Fans, ipmiFans...)
	hw.PSUs = append(hw.PSUs, ipmiPSUs...)
	if len(hw.Fans) == 0 {
		hw.Notes = append(hw.Notes, "Sensor fan tidak tersedia (Win32_Fan/IPMI)")
	}
	if len(hw.PSUs) == 0 {
		hw.Notes = append(hw.Notes, "Status PSU tidak tersedia (Win32_PowerSupply/IPMI/iLO)")
	}

	if len(hw.Storage) == 0 {
		hw.Notes = append(hw.Notes, "Get-PhysicalDisk tidak mengembalikan disk; jalankan sebagai Administrator dan cek modul Storage")
	} else {
		reliability := false
		raid := false
		for _, disk := range hw.Storage {
			if disk.TemperatureC != nil || disk.WearPercent != nil || disk.PowerOnHours != nil ||
				disk.ReadErrors > 0 || disk.WriteErrors > 0 {
				reliability = true
			}
			if strings.Contains(strings.ToLower(disk.Bus), "raid") {
				raid = true
			}
		}
		if !reliability {
			hw.Notes = append(hw.Notes, "StorageReliabilityCounter tidak memberi suhu/wear/POH; jalankan sebagai Administrator")
		}
		if raid {
			hw.Notes = append(hw.Notes, "HPE Smart Array dapat hanya mengekspos logical volume; cek cache battery/rebuild di iLO/SSA")
		}
	}
	finalize(&hw)
	return hw
}

func runPowerShell(ctx context.Context, script string) ([]byte, error) {
	encoded := encodePowerShell(script)
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encoded)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			detail := strings.TrimSpace(string(exitErr.Stderr))
			if detail != "" {
				if len(detail) > 300 {
					detail = detail[:300]
				}
				return nil, fmt.Errorf("%v: %s", err, detail)
			}
		}
		return nil, err
	}
	out = decodePowerShellOutput(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("PowerShell tidak mengembalikan data")
	}
	return out, nil
}

func decodePowerShellOutput(out []byte) []byte {
	// Windows PowerShell may write UTF-16LE when redirected, despite
	// Console.OutputEncoding; handle both BOM and NUL-padded output.
	le := len(out) >= 2 && out[0] == 0xff && out[1] == 0xfe
	if !le && len(out) >= 4 && out[1] == 0 && out[3] == 0 {
		le = true
	}
	if le {
		start := 0
		if len(out) >= 2 && out[0] == 0xff && out[1] == 0xfe {
			start = 2
		}
		units := make([]uint16, 0, (len(out)-start)/2)
		for i := start; i+1 < len(out); i += 2 {
			units = append(units, binary.LittleEndian.Uint16(out[i:]))
		}
		out = []byte(string(utf16.Decode(units)))
	}
	text := strings.TrimPrefix(string(out), "\ufeff")
	return []byte(strings.TrimSpace(text))
}

func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(buf[i*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

const windowsHardwareScript = `
$ErrorActionPreference = 'SilentlyContinue'
$ProgressPreference = 'SilentlyContinue'
$OutputEncoding = [System.Text.Encoding]::UTF8
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

$board = Get-CimInstance -ClassName Win32_BaseBoard | Select-Object Manufacturer,Product,SerialNumber
$bios = Get-CimInstance -ClassName Win32_BIOS | Select-Object Manufacturer,SMBIOSBIOSVersion,@{n='ReleaseDate';e={ if ($_.ReleaseDate) { $_.ReleaseDate.ToString('yyyy-MM-dd') } }}
$sys = Get-CimInstance -ClassName Win32_ComputerSystem | Select-Object Manufacturer,Model,TotalPhysicalMemory
$cpus = @(Get-CimInstance -ClassName Win32_Processor | Select-Object Name,ProcessorId,NumberOfCores,NumberOfLogicalProcessors,MaxClockSpeed,CurrentClockSpeed)
$cpuTemp = $null
try {
  $zones = @(Get-CimInstance -Namespace root/wmi -ClassName MSAcpi_ThermalZoneTemperature)
  if ($zones.Count -gt 0) {
    $max = ($zones | Measure-Object -Property CurrentTemperature -Maximum).Maximum
    if ($max) { $cpuTemp = [math]::Round(($max / 10) - 273.15, 1) }
  }
} catch {}
$memory = @(Get-CimInstance -ClassName Win32_PhysicalMemory | Select-Object DeviceLocator,BankLabel,Capacity,Speed,ConfiguredClockSpeed,Manufacturer,PartNumber,SerialNumber)

$disks = @()
foreach ($d in @(Get-PhysicalDisk)) {
  $r = $d | Get-StorageReliabilityCounter
  $disks += [pscustomobject]@{
    FriendlyName      = [string]$d.FriendlyName
    MediaType         = [string]$d.MediaType
    BusType           = [string]$d.BusType
    Size              = $d.Size
    HealthStatus      = [string]$d.HealthStatus
    OperationalStatus = (@($d.OperationalStatus) -join ', ')
    SerialNumber      = [string]$d.SerialNumber
    Temperature       = $r.Temperature
    Wear              = $r.Wear
    PowerOnHours      = $r.PowerOnHours
    ReadErrorsTotal   = $r.ReadErrorsTotal
    WriteErrorsTotal  = $r.WriteErrorsTotal
  }
}

$gpus = @(Get-CimInstance -ClassName Win32_VideoController | ForEach-Object {
  [pscustomobject]@{
    Name   = [string]$_.Name
    Vendor = [string]$_.AdapterCompatibility
    Memory = $_.AdapterRAM
    Driver = [string]$_.DriverVersion
    Status = [string]$_.Status
  }
})
$npus = @()
try {
  $npus = @(Get-PnpDevice -Class ComputeAccelerator -ErrorAction SilentlyContinue | ForEach-Object {
    [pscustomobject]@{ Name = [string]$_.FriendlyName; Status = [string]$_.Status }
  })
} catch {}
$fans = @(Get-CimInstance -ClassName Win32_Fan | ForEach-Object {
  [pscustomobject]@{ Name = [string]$_.Name; Status = [string]$_.Status; DesiredSpeed = $_.DesiredSpeed }
})
$psus = @()
try {
  $psus = @(Get-CimInstance -ClassName Win32_PowerSupply | ForEach-Object {
    [pscustomobject]@{ Name = [string]$_.Name; Status = [string]$_.Status; ActivePower = $_.ActivePower }
  })
} catch {}

$whea = 0
try {
  $whea = @(Get-WinEvent -FilterHashtable @{LogName='System'; ProviderName='Microsoft-Windows-WHEA-Logger'; StartTime=(Get-Date).AddDays(-7)} -MaxEvents 100 -ErrorAction SilentlyContinue).Count
} catch {}

[pscustomobject]@{
  Board      = $board
  Bios       = $bios
  System     = $sys
  CPU        = $cpus
  CpuTemp    = $cpuTemp
  Memory     = $memory
  GPUs       = $gpus
  NPUs       = $npus
  Fans       = $fans
  PSUs       = $psus
  Disks      = $disks
  WheaErrors = [int]$whea
} | ConvertTo-Json -Depth 6 -Compress
`
