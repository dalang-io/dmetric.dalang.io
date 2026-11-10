//go:build !windows

package prereq

import "os"

func isElevated() bool { return os.Geteuid() == 0 }
