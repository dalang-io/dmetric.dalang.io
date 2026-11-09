package monitor

import "testing"

func TestLossNeedsThreeAttempts(t *testing.T) {
	p := newNetworkProber(nil)
	for attempt := 1; attempt <= 2; attempt++ {
		loss, attempts := p.record("tcp|test", false)
		if loss != 0 || attempts != attempt {
			t.Fatalf("attempt %d: loss=%v attempts=%d, want 0/%d", attempt, loss, attempts, attempt)
		}
	}
	loss, attempts := p.record("tcp|test", false)
	if loss != 100 || attempts != 3 {
		t.Fatalf("third failure: loss=%v attempts=%d, want 100/3", loss, attempts)
	}
	loss, attempts = p.record("tcp|test", true)
	if loss != 75 || attempts != 4 {
		t.Fatalf("fourth success: loss=%v attempts=%d, want 75/4", loss, attempts)
	}
}
