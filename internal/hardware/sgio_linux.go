//go:build linux

package hardware

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"dmetric.dalang.io/internal/model"
)

// SG_IO ioctl (linux/sg.h). Both SMART commands below are read-only: they
// are the same commands smartctl issues and change nothing on the drive.
const (
	sgIO             = 0x2285
	sgDxferNone      = -1
	sgDxferFromDev   = -3
	sgInterfaceID    = 'S'
	ataPassThrough16 = 0x85
)

// sgIOHdr mirrors struct sg_io_hdr (64-bit layout; pointers as uintptr).
type sgIOHdr struct {
	InterfaceID    int32
	DxferDirection int32
	CmdLen         uint8
	MxSbLen        uint8
	IovecCount     uint16
	DxferLen       uint32
	Dxferp         uintptr
	Cmdp           uintptr
	Sbp            uintptr
	Timeout        uint32
	Flags          uint32
	PackID         int32
	UsrPtr         uintptr
	Status         uint8
	MaskedStatus   uint8
	MsgStatus      uint8
	SbLenWr        uint8
	HostStatus     uint16
	DriverStatus   uint16
	Resid          int32
	Duration       uint32
	Info           uint32
}

func sgRun(fd uintptr, cdb []byte, data []byte, sense []byte, dir int32) error {
	hdr := sgIOHdr{
		InterfaceID:    sgInterfaceID,
		DxferDirection: dir,
		CmdLen:         uint8(len(cdb)),
		MxSbLen:        uint8(len(sense)),
		DxferLen:       uint32(len(data)),
		Cmdp:           uintptr(unsafe.Pointer(&cdb[0])),
		Sbp:            uintptr(unsafe.Pointer(&sense[0])),
		Timeout:        3000, // ms
	}
	if len(data) > 0 {
		hdr.Dxferp = uintptr(unsafe.Pointer(&data[0]))
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, sgIO, uintptr(unsafe.Pointer(&hdr)))
	runtime.KeepAlive(cdb)
	runtime.KeepAlive(data)
	runtime.KeepAlive(sense)
	if errno != 0 {
		return errno
	}
	// A pass-through the HBA/drive rejects shows up here, not in errno.
	if hdr.HostStatus != 0 || hdr.DriverStatus&0x07 != 0 {
		return fmt.Errorf("SG_IO ditolak (host=%#x driver=%#x)", hdr.HostStatus, hdr.DriverStatus)
	}
	return nil
}

// readATASmart reads the SMART data page and the pass/fail verdict from a
// SATA drive (directly attached or through a SAT-capable HBA). Root needed.
func readATASmart(device string) ([]ataAttribute, int, error) {
	f, err := os.OpenFile(device, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fd := f.Fd()

	return readATAWith(func(feature uint8, wantData bool) ([]byte, uint8, uint8, error) {
		sense := make([]byte, 32)
		if wantData {
			// PIO data-in, 1 sector; features=feature, lba mid/high=4F/C2.
			page := make([]byte, 512)
			cdb := []byte{
				ataPassThrough16, 4 << 1, 0x0e, // protocol PIO data-in; t_dir=in, byte-block, sector-count
				0, feature, // features
				0, 1, // sector count
				0, 0, // lba low
				0, smartLBAMid,
				0, smartLBAHigh,
				0, ataCmdSmart, 0,
			}
			return page, 0, 0, sgRun(fd, cdb, page, sense, sgDxferFromDev)
		}
		// Non-data; ck_cond makes the drive return its registers in the
		// sense data, which carry the verdict.
		cdb := []byte{
			ataPassThrough16, 3 << 1, 0x20, // protocol non-data; ck_cond
			0, feature,
			0, 0,
			0, 0,
			0, smartLBAMid,
			0, smartLBAHigh,
			0, ataCmdSmart, 0,
		}
		if err := sgRun(fd, cdb, nil, sense, sgDxferNone); err != nil {
			return nil, 0, 0, err
		}
		mid, high, ok := senseRegisters(sense)
		if !ok {
			return nil, 0, 0, fmt.Errorf("sense data tanpa register ATA")
		}
		return nil, mid, high, nil
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
