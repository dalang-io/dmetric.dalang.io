//go:build windows

package hardware

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"dmetric.dalang.io/internal/model"
)

// Native ATA SMART on Windows through IOCTL_ATA_PASS_THROUGH. It works for
// SATA drives the OS sees directly (AHCI, or a Smart Array in HBA mode). It
// cannot see drives behind a RAID logical volume; those come from ssacli.
// Needs Administrator. The commands are the same read-only SMART commands as
// the Linux transport.

const (
	ioctlATAPassThrough = 0x0004d02c
	ataFlagsDRDY        = 0x01
	ataFlagsDataIn      = 0x02
)

// ataPassThroughEx mirrors ATA_PASS_THROUGH_EX (64-bit layout, 48 bytes).
type ataPassThroughEx struct {
	Length             uint16
	AtaFlags           uint16
	PathID             uint8
	TargetID           uint8
	Lun                uint8
	ReservedAsUchar    uint8
	DataTransferLength uint32
	TimeOutValue       uint32
	ReservedAsUlong    uint32
	DataBufferOffset   uintptr
	PreviousTaskFile   [8]byte // features, count, lba low, mid, high, device, command, reserved
	CurrentTaskFile    [8]byte // error, count, lba low, mid, high, device, status, reserved
}

// readATASmart opens \\.\PhysicalDriveN and reads SMART.
func readATASmart(device string) ([]ataAttribute, int, error) {
	path, err := windows.UTF16PtrFromString(device)
	if err != nil {
		return nil, 0, err
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("buka %s: %w (butuh Administrator)", device, err)
	}
	defer windows.CloseHandle(handle)

	return readATAWith(func(feature uint8, wantData bool) ([]byte, uint8, uint8, error) {
		hdrSize := int(unsafe.Sizeof(ataPassThroughEx{}))
		dataLen := 0
		if wantData {
			dataLen = 512
		}
		buf := make([]byte, hdrSize+dataLen)
		hdr := (*ataPassThroughEx)(unsafe.Pointer(&buf[0]))
		hdr.Length = uint16(hdrSize)
		hdr.AtaFlags = ataFlagsDRDY
		if wantData {
			hdr.AtaFlags |= ataFlagsDataIn
		}
		hdr.DataTransferLength = uint32(dataLen)
		hdr.TimeOutValue = 3 // seconds
		hdr.DataBufferOffset = uintptr(hdrSize)
		hdr.PreviousTaskFile = [8]byte{feature, 1, 0, smartLBAMid, smartLBAHigh, 0xa0, ataCmdSmart, 0}
		if !wantData {
			hdr.PreviousTaskFile[1] = 0 // sector count unused by RETURN STATUS
		}

		var returned uint32
		err := windows.DeviceIoControl(handle, ioctlATAPassThrough,
			&buf[0], uint32(len(buf)), &buf[0], uint32(len(buf)), &returned, nil)
		if err != nil {
			return nil, 0, 0, err
		}
		out := (*ataPassThroughEx)(unsafe.Pointer(&buf[0]))
		// CurrentTaskFile status register: bit 0 = error, bit 3 = DRQ stuck.
		if out.CurrentTaskFile[6]&0x01 != 0 {
			return nil, 0, 0, fmt.Errorf("drive menolak perintah SMART (error=%#x)", out.CurrentTaskFile[0])
		}
		var page []byte
		if wantData {
			page = append([]byte(nil), buf[hdrSize:]...)
		}
		return page, out.CurrentTaskFile[3], out.CurrentTaskFile[4], nil
	})
}

// nativeATAHealth fills disk from the in-process SMART reader.
func nativeATAHealth(disk *model.HWStorage, device string) error {
	attrs, verdict, err := readATASmart(device)
	if err != nil {
		return err
	}
	applyATASmart(disk, attrs, verdict)
	disk.HealthNote = ""
	if verdict == smartVerdictUnknown {
		disk.HealthNote = "SMART terbaca, tetapi vonis lulus/gagal tidak dikembalikan drive"
	}
	return nil
}
