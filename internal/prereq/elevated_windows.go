//go:build windows

package prereq

import (
	"os/exec"
	"strings"
)

func isElevated() bool {
	out, err := exec.Command("whoami", "/groups").Output()
	if err != nil {
		return false
	}
	text := string(out)
	return strings.Contains(text, "S-1-16-12288") ||
		strings.Contains(text, "S-1-16-16384")
}
