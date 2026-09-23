package wager_test

import (
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestPendingPolicyBackoff(t *testing.T) {
	p := wager.PendingPolicy{MaxAttempts: 8, BaseDelay: 2 * time.Second, MaxDelay: 30 * time.Second}
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{-1, 2 * time.Second},
		{0, 2 * time.Second},
		{1, 4 * time.Second},
		{2, 8 * time.Second},
		{3, 16 * time.Second},
		{4, 30 * time.Second},
		{50, 30 * time.Second},
		{1 << 30, 30 * time.Second},
	}
	for _, c := range cases {
		if got := p.Delay(c.attempts); got != c.want {
			t.Errorf("Delay(%d) = %v, want %v", c.attempts, got, c.want)
		}
	}
	if got := p.NextAttemptAt(1, t0); !got.Equal(t0.Add(4 * time.Second)) {
		t.Errorf("NextAttemptAt = %v", got)
	}
	huge := wager.PendingPolicy{MaxAttempts: 1, BaseDelay: time.Hour, MaxDelay: time.Duration(1<<63 - 1)}
	if got := huge.Delay(100); got <= 0 {
		t.Errorf("delay overflowed to %v", got)
	}
}

func TestPendingPolicyValidation(t *testing.T) {
	if err := wager.DefaultPendingPolicy().Validate(); err != nil {
		t.Fatal(err)
	}
	d := wager.DefaultPendingPolicy()
	if d.MaxAttempts != 8 {
		t.Errorf("default max attempts = %d, want 8", d.MaxAttempts)
	}
	bad := []wager.PendingPolicy{
		{MaxAttempts: 0, BaseDelay: time.Second, MaxDelay: time.Second},
		{MaxAttempts: 1, BaseDelay: 0, MaxDelay: time.Second},
		{MaxAttempts: 1, BaseDelay: time.Minute, MaxDelay: time.Second},
	}
	for _, p := range bad {
		if p.Validate() == nil {
			t.Errorf("%+v must be invalid", p)
		}
	}
}

func TestRecordReferenceMissExpiresWithReferenceNotFound(t *testing.T) {
	p := wager.PendingPolicy{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: time.Minute}
	tx := newTx(t, op(t, wager.KindRollback, "rb-1", "25.00", "bet-1"))
	mustApply(t, tx, openWallet(t, "100.00"), wager.Reference{}, wager.StatusPendingReference)
	if !p.Exhausted(3) || p.Exhausted(2) {
		t.Fatal("Exhausted boundary")
	}

	now := t0
	for attempt := 1; attempt <= 2; attempt++ {
		now = now.Add(time.Minute)
		next, exhausted, err := tx.RecordReferenceMiss(p, now)
		if err != nil || exhausted {
			t.Fatalf("attempt %d: %v %v", attempt, exhausted, err)
		}
		if tx.Attempts() != attempt || tx.Status() != wager.StatusPendingReference {
			t.Fatalf("attempt %d: attempts %d status %s", attempt, tx.Attempts(), tx.Status())
		}
		if want := now.Add(p.Delay(attempt)); !next.Equal(want) {
			t.Fatalf("attempt %d: next %v, want %v", attempt, next, want)
		}
	}
	next, exhausted, err := tx.RecordReferenceMiss(p, now)
	if err != nil || !exhausted || !next.IsZero() {
		t.Fatalf("last attempt: %v %v %v", next, exhausted, err)
	}
	if tx.Status() != wager.StatusRejected || tx.FailureCode() != wager.FailureReferenceNotFound || tx.Attempts() != 3 {
		t.Fatalf("status %s code %s attempts %d", tx.Status(), tx.FailureCode(), tx.Attempts())
	}
	if _, ok := tx.BalanceAfter(); ok {
		t.Error("REFERENCE_NOT_FOUND reports no balance")
	}
	if _, err := wager.Rehydrate(tx.Snapshot()); err != nil {
		t.Fatalf("expired transaction does not rehydrate: %v", err)
	}
	if _, _, err := tx.RecordReferenceMiss(p, now); !errors.Is(err, wager.ErrInvalidTransition) {
		t.Errorf("miss after expiry error = %v", err)
	}
}

func TestRecordReferenceMissRequiresPendingReference(t *testing.T) {
	tx := newTx(t, op(t, wager.KindRollback, "rb-1", "25.00", "bet-1"))
	if _, _, err := tx.RecordReferenceMiss(wager.DefaultPendingPolicy(), t0); !errors.Is(err, wager.ErrInvalidTransition) {
		t.Errorf("PENDING error = %v", err)
	}
	_ = tx.AwaitReference(t0)
	if _, _, err := tx.RecordReferenceMiss(wager.PendingPolicy{}, t0); err == nil {
		t.Error("invalid policy must be rejected")
	}
}
