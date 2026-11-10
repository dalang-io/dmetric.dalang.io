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

func TestInstallToKeepsPreviousBinary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dmetric")
	os.WriteFile(target, []byte("old binary"), 0o755)
	source := filepath.Join(dir, "download")
	os.WriteFile(source, []byte("new binary"), 0o644)
	if _, err := installTo(source, target); err != nil {
		t.Fatal(err)
	}
	prev, err := os.ReadFile(target + ".prev")
	if err != nil || string(prev) != "old binary" {
		t.Fatalf("rollback copy missing or wrong: %q err=%v", prev, err)
	}
}

func TestSelfInstallBlocker(t *testing.T) {
	for _, exe := range []string{"/Applications/DMetric.app/Contents/MacOS/DMetric", "/tmp/go-build123/b001/exe/main"} {
		if selfInstallBlocker(exe) == nil {
			t.Errorf("%s must be blocked", exe)
		}
	}
	if err := selfInstallBlocker("/usr/local/bin/dmetric"); err != nil {
		t.Errorf("standalone binary blocked: %v", err)
	}
}
