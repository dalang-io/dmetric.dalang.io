package diskid

import (
	"os"
	"path/filepath"
	"testing"
)

// tree mirrors the by-id layout captured on a real Dell R630 hypervisor.
func tree(t *testing.T) (byID, sysBlock string) {
	t.Helper()
	root := t.TempDir()
	byID = filepath.Join(root, "by-id")
	sysBlock = filepath.Join(root, "block")
	for _, d := range []string{"sda", "sdb", "sdc", "sdd", "sde", "nvme0n1", "dm-0", "rbd0"} {
		if err := os.MkdirAll(filepath.Join(root, "dev", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(byID, 0o755); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"ata-KYO_2025122000004":                        "sdd",
		"ata-SSD_1TB_0926270101311015342":              "sdc",
		"wwn-0x0000000000000000":                       "sdc", // placeholder WWN
		"wwn-0x5002538c4069e13a":                       "sdb",
		"scsi-35002538c4069e13a":                       "sdb",
		"wwn-0x5002538e1024925d":                       "sda",
		"scsi-35002538e1024925d":                       "sda",
		"scsi-361866da070f3c200326eccc2059fdc82":       "sde",
		"wwn-0x61866da070f3c200326eccc2059fdc82":       "sde",
		"wwn-0x61866da070f3c200326eccc2059fdc82-part2": "sde",
		"nvme-TEAM_TM8FPW004T_TPBF2503130090100614":    "nvme0n1",
		"nvme-TEAM_TM8FPW004T_TPBF2503130090100614_1":  "nvme0n1",
		"nvme-eui.00000000000000000c82d50090100614":    "nvme0n1",
		"lvm-pv-uuid-UK2ixE-cSW8":                      "sda",
		"dm-name-ceph--1c2d":                           "dm-0",
	}
	for alias, dev := range links {
		if err := os.Symlink(filepath.Join(root, "dev", dev), filepath.Join(byID, alias)); err != nil {
			t.Fatal(err)
		}
	}
	return byID, sysBlock
}

func TestStableIDPriority(t *testing.T) {
	byID, _ := tree(t)
	idx := Load(byID)
	cases := map[string]string{
		"sda":     "wwn-0x5002538e1024925d",
		"sdb":     "wwn-0x5002538c4069e13a",
		"sdc":     "ata-SSD_1TB_0926270101311015342", // all-zero WWN rejected
		"sdd":     "ata-KYO_2025122000004",           // no WWN at all
		"sde":     "wwn-0x61866da070f3c200326eccc2059fdc82",
		"nvme0n1": "nvme-eui.00000000000000000c82d50090100614",
		"dm-0":    "", // device-mapper alias is not a hardware identity
	}
	for kernel, want := range cases {
		if got := idx.ID(kernel); got != want {
			t.Errorf("ID(%s) = %q, want %q", kernel, got, want)
		}
	}
}

func TestResolveFallbackAndCollision(t *testing.T) {
	byID, sys := tree(t)
	// sdz has no by-id alias; sysfs model+serial should be used.
	dev := filepath.Join(sys, "sdz", "device")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dev, "model"), []byte("FOO BAR\n"), 0o644)
	os.WriteFile(filepath.Join(dev, "serial"), []byte("123\n"), 0o644)
	// sdy has nothing at all -> kernel name.
	os.MkdirAll(filepath.Join(sys, "sdy"), 0o755)

	got := Load(byID).Resolve(sys, []string{"sda", "sdz", "sdy"})
	if got["sda"] != "wwn-0x5002538e1024925d" || got["sdz"] != "FOO_BAR_123" || got["sdy"] != "sdy" {
		t.Fatalf("unexpected: %v", got)
	}

	// Two disks reporting the same sysfs identity stay distinguishable.
	for _, n := range []string{"sdp", "sdq"} {
		d := filepath.Join(sys, n, "device")
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "model"), []byte("CLONE"), 0o644)
		os.WriteFile(filepath.Join(d, "serial"), []byte("SAME"), 0o644)
	}
	got = Load(byID).Resolve(sys, []string{"sdp", "sdq"})
	if got["sdp"] == got["sdq"] {
		t.Fatalf("collision not resolved: %v", got)
	}
}

func TestPartitionIDAndParent(t *testing.T) {
	byID, _ := tree(t)
	if PartitionParent("sde2") != "sde" || PartitionParent("nvme0n1p3") != "nvme0n1" || PartitionParent("sda") != "" {
		t.Fatal("PartitionParent")
	}
	if got := Load(byID).ID("sde"); got == "" {
		t.Fatal("missing sde")
	}
}

func TestVirtualAndStacked(t *testing.T) {
	for _, n := range []string{"loop3", "rbd0", "rbd69", "zram0", "sr0"} {
		if !Virtual(n) || Physical(n) {
			t.Errorf("%s should be virtual", n)
		}
	}
	for _, n := range []string{"dm-0", "md127", "bcache0", "nbd1"} {
		if !Stacked(n) || Physical(n) {
			t.Errorf("%s should be stacked", n)
		}
	}
	for _, n := range []string{"sda", "nvme0n1", "vda"} {
		if !Physical(n) {
			t.Errorf("%s should be physical", n)
		}
	}
}
