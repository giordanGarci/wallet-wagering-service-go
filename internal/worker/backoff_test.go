package worker_test

import (
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/worker"
)

func TestBackoffGrowsExponentiallyUpToTheCap(t *testing.T) {
	b := worker.Backoff{Base: time.Second, Max: 10 * time.Second}
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Second}, {1, time.Second}, {2, 2 * time.Second}, {3, 4 * time.Second},
		{4, 8 * time.Second}, {5, 10 * time.Second}, {60, 10 * time.Second}, {1 << 30, 10 * time.Second},
	}
	for _, tt := range tests {
		if got := b.Ceiling(tt.attempt); got != tt.want {
			t.Errorf("Ceiling(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestBackoffDelayIsJitteredWithinHalfAndCeiling(t *testing.T) {
	b := worker.Backoff{Base: 100 * time.Millisecond, Max: time.Second}
	for attempt := 1; attempt <= 6; attempt++ {
		ceiling := b.Ceiling(attempt)
		seen := map[time.Duration]bool{}
		for range 200 {
			d := b.Delay(attempt)
			if d < ceiling/2 || d > ceiling {
				t.Fatalf("Delay(%d) = %s, want within [%s, %s]", attempt, d, ceiling/2, ceiling)
			}
			seen[d] = true
		}
		if len(seen) < 2 {
			t.Errorf("Delay(%d) returned a constant %v, want jitter", attempt, seen)
		}
	}
}
