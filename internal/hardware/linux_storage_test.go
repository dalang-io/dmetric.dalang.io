//go:build linux

package hardware

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A hypervisor-like tree: real disks plus Ceph RBD and device-mapper nodes.
func TestLinuxStorageSkipsRBDAndUsesStableID(t *testing.T) {
	root := t.TempDir()
	sys := filepath.Join(root, "block")
	byID := filepath.Join(root, "by-id")
	os.MkdirAll(byID, 0o755)
	mk := func(name, model, serial, rota string) {
		dir := filepath.Join(sys, name)
		os.MkdirAll(filepath.Join(dir, "device"), 0o755)
		os.MkdirAll(filepath.Join(dir, "queue"), 0o755)
		os.WriteFile(filepath.Join(dir, "device", "model"), []byte(model), 0o644)
		os.WriteFile(filepath.Join(dir, "device", "serial"), []byte(serial), 0o644)
		os.WriteFile(filepath.Join(dir, "queue", "rotational"), []byte(rota), 0o644)
		os.WriteFile(filepath.Join(dir, "size"), []byte("2000000"), 0o644)
	}
	mk("sda", "MZ7KH480", "S5CN", "0")
	mk("sde", "PERC H330 Mini", "0082", "1")
	for _, n := range []string{"rbd0", "rbd1", "loop0", "dm-0", "md0"} {
		mk(n, "", "", "0")
	}
	os.Symlink(filepath.Join(sys, "sda"), filepath.Join(byID, "wwn-0x5002538e1024925d"))

	var notes []string
	got := linuxStorageAt(context.Background(), &notes, sys, byID)
	if len(got) < 2 {
		t.Fatalf("got %d disks: %+v", len(got), got)
	}
	seen := map[string]string{}
	for _, d := range got {
		seen[d.Name] = d.ID
	}
	for _, bad := range []string{"rbd0", "rbd1", "loop0", "dm-0", "md0"} {
		if _, ok := seen[bad]; ok {
			t.Errorf("%s must not be listed as a physical disk", bad)
		}
	}
	if seen["sda"] != "wwn-0x5002538e1024925d" {
		t.Errorf("sda id = %q", seen["sda"])
	}
	for _, d := range got {
		if d.Name == "sde" && (d.Bus != "RAID" || d.Media != "RAID virtual" || d.HealthNote == "") {
			t.Errorf("PERC virtual disk not labelled: %+v", d)
		}
	}
}
