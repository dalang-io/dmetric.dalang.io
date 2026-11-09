// Package diskid gives block devices a name that survives reboots.
//
// Kernel names (sda, sdb, nvme1n1) are assigned in probe order, so the same
// physical disk can be sdb today and sdd after a restart. Anything an operator
// acts on (replace this disk, this volume is slow) must therefore be shown by
// a stable identity: the WWN/EUI, or the model_serial alias udev keeps under
// /dev/disk/by-id.
package diskid

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Default locations; tests point them at a temporary tree.
const (
	DefaultByIDDir  = "/dev/disk/by-id"
	DefaultSysBlock = "/sys/block"
)

// Virtual reports block devices that are not physical hardware: pseudo
// devices, optical drives and network block devices such as Ceph RBD, which
// can number in the dozens on a hypervisor and only add noise.
func Virtual(name string) bool {
	for _, prefix := range []string{"loop", "ram", "zram", "sr", "fd", "rbd"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// Stacked reports devices layered on top of other block devices (LVM/LUKS,
// software RAID, bcache, DRBD, NBD). Their I/O is already counted on the
// physical parent.
func Stacked(name string) bool {
	for _, prefix := range []string{"dm-", "md", "bcache", "drbd", "nbd"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// Physical reports whether name can be a physical disk.
func Physical(name string) bool {
	return !Virtual(name) && !Stacked(name)
}

// Index maps kernel device names to their most stable identity.
type Index struct {
	ids map[string]string
}

// Load scans byIDDir once. Missing directories give an empty index, so
// callers fall back to Fallback.
func Load(byIDDir string) *Index {
	idx := &Index{ids: map[string]string{}}
	entries, err := os.ReadDir(byIDDir)
	if err != nil {
		return idx
	}
	type candidate struct {
		alias string
		rank  int
	}
	best := map[string]candidate{}
	for _, entry := range entries {
		alias := entry.Name()
		rank, ok := aliasRank(alias)
		if !ok {
			continue
		}
		target, err := filepath.EvalSymlinks(filepath.Join(byIDDir, alias))
		if err != nil {
			continue
		}
		kernel := filepath.Base(target)
		cur, exists := best[kernel]
		if !exists || rank < cur.rank ||
			(rank == cur.rank && (len(alias) < len(cur.alias) ||
				(len(alias) == len(cur.alias) && alias < cur.alias))) {
			best[kernel] = candidate{alias: alias, rank: rank}
		}
	}
	for kernel, c := range best {
		idx.ids[kernel] = c.alias
	}
	return idx
}

// ID returns the stable identity of a device or partition ("" when udev
// published none).
func (i *Index) ID(kernel string) string {
	if i == nil {
		return ""
	}
	return i.ids[kernel]
}

// Ranks, lower wins: a real WWN or EUI is globally unique; model_serial
// aliases are readable and unique per drive; scsi-3<naa> mirrors the WWN.
const (
	rankWWN = iota
	rankEUI
	rankModelSerial
	rankSCSI
)

// aliasRank rates a /dev/disk/by-id alias. ok is false for aliases that do
// not identify hardware (LVM/device-mapper/md) or carry a placeholder WWN.
func aliasRank(alias string) (rank int, ok bool) {
	lower := strings.ToLower(alias)
	for _, prefix := range []string{"dm-", "lvm-", "md-", "mpath-"} {
		if strings.HasPrefix(lower, prefix) {
			return 0, false
		}
	}
	switch {
	case strings.HasPrefix(lower, "wwn-"):
		if placeholder(strings.TrimPrefix(lower, "wwn-")) {
			return 0, false
		}
		return rankWWN, true
	case strings.HasPrefix(lower, "nvme-eui."):
		if placeholder(strings.TrimPrefix(lower, "nvme-eui.")) {
			return 0, false
		}
		return rankEUI, true
	case strings.HasPrefix(lower, "scsi-3"):
		if placeholder(strings.TrimPrefix(lower, "scsi-3")) {
			return 0, false
		}
		return rankSCSI, true
	}
	return rankModelSerial, true
}

// placeholder is true for all-zero identifiers that some cheap SSDs and
// controllers report instead of a real WWN (wwn-0x0000000000000000).
func placeholder(value string) bool {
	value = strings.TrimPrefix(value, "0x")
	if i := strings.Index(value, "-part"); i >= 0 {
		value = value[:i]
	}
	value = strings.NewReplacer("-", "", ".", "").Replace(value)
	return strings.Trim(value, "0") == ""
}

// Fallback builds an identity from sysfs model and serial when udev has no
// by-id alias, finally the kernel name. Never empty.
func Fallback(sysBlock, kernel string) string {
	base := filepath.Join(sysBlock, kernel)
	for _, rel := range []string{"wwid", filepath.Join("device", "wwid")} {
		if v := readTrim(filepath.Join(base, rel)); v != "" && !placeholder(strings.ToLower(v)) {
			return strings.NewReplacer(" ", "_", "/", "_").Replace(v)
		}
	}
	model := readTrim(filepath.Join(base, "device", "model"))
	serial := readTrim(filepath.Join(base, "device", "serial"))
	if model != "" && serial != "" {
		return strings.NewReplacer(" ", "_", "/", "_").Replace(model + "_" + serial)
	}
	return kernel
}

// Resolve returns a stable identity for every kernel name given, resolving
// collisions (two disks claiming one ID) by keeping the kernel name visible.
func (i *Index) Resolve(sysBlock string, kernelNames []string) map[string]string {
	out := make(map[string]string, len(kernelNames))
	count := map[string]int{}
	for _, name := range kernelNames {
		id := i.ID(name)
		if id == "" {
			id = Fallback(sysBlock, name)
		}
		out[name] = id
		count[id]++
	}
	dups := make([]string, 0)
	for name, id := range out {
		if count[id] > 1 && id != name {
			dups = append(dups, name)
		}
	}
	sort.Strings(dups)
	for _, name := range dups {
		out[name] = out[name] + " [" + name + "]"
	}
	return out
}

var partitionSuffixes = []*regexp.Regexp{
	regexp.MustCompile(`^(nvme\d+n\d+)p\d+$`),
	regexp.MustCompile(`^(mmcblk\d+)p\d+$`),
	regexp.MustCompile(`^((?:sd|vd|xvd|hd)[a-z]+)\d+$`),
}

// PartitionParent turns sda1 / nvme0n1p2 / mmcblk0p1 into the parent disk
// name. It returns "" for names that are not partitions.
func PartitionParent(name string) string {
	for _, p := range partitionSuffixes {
		if m := p.FindStringSubmatch(name); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

func readTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
