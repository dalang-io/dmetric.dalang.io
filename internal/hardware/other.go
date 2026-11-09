//go:build !windows && !linux && !darwin

package hardware

import (
	"context"

	"dmetric.dalang.io/internal/model"
)

func collect(_ context.Context) model.Hardware {
	return empty("Hardware", "pemeriksaan hardware belum didukung di platform ini")
}
