//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/postgres"
)

// Instances stamp Ledger Entries with their own clocks. An entry appended by
// an instance whose clock lags must still sort after the Wallet's previous
// entry, so the (created_at, id) order follows the balance chain.
func TestAppendKeepsLedgerOrderUnderClockSkew(t *testing.T) {
	s := newSchema(t)
	ctx := context.Background()
	repo := postgres.NewLedgerRepository(s.app)
	brl, _ := money.ParseCurrency("BRL")
	minor := func(n int64) money.Money { m, _ := money.FromMinorUnits(n, brl); return m }

	now := time.Now()
	balances := []int64{10000, 9900, 9800}
	clocks := []time.Time{now.Add(time.Hour), now.Add(-time.Hour)} // second instance lags two hours
	var (
		want   []uuid.UUID
		stored []ledger.Entry // what Append reports as written
	)
	for i, at := range clocks {
		txID := uuid.Must(uuid.NewV7())
		s.mustExec(t, s.owner, insertExternal, txID, s.walletID, s.playerID, "BET", "PROCESSED", "provider-a",
			"bet-"+string(rune('a'+i)), "key-"+string(rune('a'+i)), nil, nil)
		e, err := ledger.New(s.walletID, txID, ledger.Debit, minor(100), minor(balances[i]), minor(balances[i+1]), at)
		if err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		saved, err := repo.Append(ctx, e)
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		want = append(want, e.ID())
		stored = append(stored, saved)
	}

	got, err := repo.List(ctx, s.walletID, nil, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 || got[0].ID() != s.entryID || got[1].ID() != want[0] || got[2].ID() != want[1] {
		t.Fatalf("ledger order = %v, want opening then the two debits in append order", ids(got))
	}
	for i := 1; i < len(got); i++ {
		if !got[i].BalanceBefore().Equal(got[i-1].BalanceAfter()) {
			t.Fatalf("entry %d does not chain: before %v, previous after %v", i, got[i].BalanceBefore(), got[i-1].BalanceAfter())
		}
	}
	// Append reports the created_at it wrote, which callers stamp on the
	// events: the lagging entry was pushed forward.
	for i, s := range stored {
		if s.ID() != want[i] || !s.CreatedAt().Equal(got[i+1].CreatedAt()) {
			t.Errorf("append %d reported created_at %v, stored %v", i, s.CreatedAt(), got[i+1].CreatedAt())
		}
	}
	if !stored[1].CreatedAt().After(stored[0].CreatedAt()) {
		t.Errorf("lagging entry created_at %v, want after %v", stored[1].CreatedAt(), stored[0].CreatedAt())
	}
}

func ids(es []ledger.Entry) []uuid.UUID {
	out := make([]uuid.UUID, len(es))
	for i, e := range es {
		out[i] = e.ID()
	}
	return out
}
