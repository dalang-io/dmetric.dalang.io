//go:build windows

package update

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

// selfInstallBlocker explains why exe cannot be replaced in place.
func selfInstallBlocker(exe string) error {
	if strings.Contains(strings.ToLower(exe), "go-build") {
		return errors.New("update otomatis membutuhkan binary hasil build, bukan `go run`")
	}
	return nil
}

func installUpdate(downloaded string) (func() error, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("menemukan executable: %w", err)
	}
	if err := selfInstallBlocker(exe); err != nil {
		return nil, err
	}
	// Prove the install directory is writable before spawning a helper that
	// would otherwise fail silently after the old process exits.
	source, err := stageWindowsUpdate(downloaded, exe)
	if err != nil {
		return nil, err
	}
	cwd, _ := os.Getwd()
	script := fmt.Sprintf(`$ErrorActionPreference='SilentlyContinue'
$target=%s
$source=%s
$oldPid=%d
$working=%s
while (Get-Process -Id $oldPid -ErrorAction SilentlyContinue) { Start-Sleep -Milliseconds 300 }
try { Copy-Item -LiteralPath $target -Destination ($target + '.prev') -Force -ErrorAction Stop } catch {}
for ($i=0; $i -lt 60; $i++) {
  try { Move-Item -LiteralPath $source -Destination $target -Force -ErrorAction Stop; break } catch { Start-Sleep -Seconds 1 }
}
Start-Process -FilePath $target -WorkingDirectory $working -ArgumentList @(%s)
`, psQuote(exe), psQuote(source), os.Getpid(), psQuote(cwd), psArgs(os.Args[1:]))
	encoded := encodePS(script)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encoded)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("menjalankan helper update: %w", err)
	}
	return func() error {
		os.Exit(0)
		return nil
	}, nil
}

func stageWindowsUpdate(downloaded, exe string) (string, error) {
	source := filepath.Join(filepath.Dir(exe), fmt.Sprintf(".dmetric-update-%d.exe", os.Getpid()))
	src, err := os.Open(downloaded)
	if err != nil {
		return "", fmt.Errorf("membuka update: %w", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(source, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", fmt.Errorf("menulis update (butuh izin pada direktori aplikasi): %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(source)
		return "", fmt.Errorf("menyalin update: %w", err)
	}
	if err := dst.Close(); err != nil {
		os.Remove(source)
		return "", err
	}
	return source, nil
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func psArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, psQuote(arg))
	}
	return strings.Join(quoted, ",")
}

func encodePS(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(buf[i*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(buf)
}
