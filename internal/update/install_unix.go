//go:build !windows

package update

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func installUpdate(downloaded string) (func() error, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("menemukan executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	lower := strings.ToLower(exe)
	if strings.Contains(lower, ".app/contents/macos/") {
		return nil, errors.New("aplikasi macOS dibundel (.app); unduh DMG dari halaman rilis")
	}
	if strings.Contains(lower, "go-build") {
		return nil, errors.New("update otomatis membutuhkan binary hasil build, bukan `go run`")
	}
	return installTo(downloaded, exe)
}

// installTo is separate from the process lookup so it can be tested with a
// temporary target.
func installTo(downloaded, target string) (func() error, error) {
	src, err := os.Open(downloaded)
	if err != nil {
		return nil, fmt.Errorf("membuka update: %w", err)
	}
	defer src.Close()

	tmp, err := os.CreateTemp(filepath.Dir(target), ".dmetric-update-*")
	if err != nil {
		return nil, fmt.Errorf("menulis update (butuh izin pada direktori aplikasi): %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("menyalin update: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return nil, fmt.Errorf("mengganti binary: %w", err)
	}
	args := append([]string{target}, os.Args[1:]...)
	return func() error {
		return syscall.Exec(target, args, os.Environ())
	}, nil
}
