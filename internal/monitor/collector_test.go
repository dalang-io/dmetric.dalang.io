package monitor

import (
	"reflect"
	"testing"

	"dmetric.dalang.io/internal/model"
)

func TestPhysicalDiskSet(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "drops stacked devices when physical parents exist",
			input: []string{"sda", "dm-0", "dm-1", "md0", "nvme0n1", "bcache0", "drbd0", "nbd0"},
			want:  []string{"sda", "nvme0n1"},
		},
		{
			name:  "drops pseudo devices",
			input: []string{"loop0", "loop1", "zram0", "ram0", "sr0", "fd0", "sda"},
			want:  []string{"sda"},
		},
		{
			name:  "falls back to stacked devices when no parent is exposed",
			input: []string{"dm-0", "dm-1", "md127"},
			want:  []string{"dm-0", "dm-1", "md127"},
		},
		{
			name:  "keeps every physical device",
			input: []string{"sda", "sdb", "nvme0n1", "mmcblk0"},
			want:  []string{"sda", "sdb", "nvme0n1", "mmcblk0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := physicalDiskSet(tc.input)
			want := make(map[string]bool, len(tc.want))
			for _, name := range tc.want {
				want[name] = true
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("physicalDiskSet(%v) = %v, want %v", tc.input, got, want)
			}
		})
	}
}

func TestSelectIndexesIncludesDiskHogs(t *testing.T) {
	all := []model.ProcessInfo{
		{PID: 1, Name: "cpu", CPUPercent: 99},
		{PID: 2, Name: "ram", MemoryRSS: 8 << 30},
		{PID: 3, Name: "reader", DiskReadBytesPerSec: floatPtr(100e6)},
		{PID: 4, Name: "writer", DiskWriteBytesPerSec: floatPtr(100e6)},
	}
	idx := selectIndexes(all, 1, 1, 1, 1)
	got := map[int32]bool{}
	for _, i := range idx {
		got[all[i].PID] = true
	}
	for _, pid := range []int32{1, 2, 3, 4} {
		if !got[pid] {
			t.Fatalf("process %d was not selected: %#v", pid, got)
		}
	}
}

func TestWholeDiskName(t *testing.T) {
	cases := map[string]string{
		"sda1":       "sda",
		"vda2":       "vda",
		"nvme0n1p1":  "nvme0n1",
		"mmcblk0p1":  "mmcblk0",
		"nvme0n1":    "",
		"md0":        "",
		"dm-0":       "",
		"no-numbers": "",
	}
	for input, want := range cases {
		if got := wholeDiskName(input); got != want {
			t.Errorf("wholeDiskName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRBDIsNotPhysical(t *testing.T) {
	got := physicalDiskSet([]string{"sda", "nvme0n1", "rbd0", "rbd69", "dm-0", "loop1"})
	if len(got) != 2 || !got["sda"] || !got["nvme0n1"] {
		t.Fatalf("physicalDiskSet = %v, want only sda and nvme0n1", got)
	}
	if !virtualVolumeDevice("/dev/rbd12") || virtualVolumeDevice("/dev/sde2") {
		t.Fatal("virtualVolumeDevice misclassified")
	}
}
