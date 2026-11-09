//go:build !windows

package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallTo(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dmetric")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "download")
	if err := os.WriteFile(source, []byte("new binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	restart, err := installTo(source, target)
	if err != nil {
		t.Fatal(err)
	}
	if restart == nil {
		t.Fatal("missing restart callback")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new binary" {
		t.Fatalf("target content = %q", data)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("target is not executable: %v", info.Mode())
	}
}
