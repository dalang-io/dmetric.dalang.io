//go:build darwin

package hardware

import (
	"context"
	"testing"
)

func TestCollectDarwin(t *testing.T) {
	hw := Collect(context.Background())
	if hw.Source == "" {
		t.Fatal("missing source")
	}
	if hw.Status == "" {
		t.Fatal("missing status")
	}
	if hw.System.Product == "" {
		t.Logf("notes: %v", hw.Notes)
	}
	t.Logf("hardware: %+v storage=%d", hw, len(hw.Storage))
}
