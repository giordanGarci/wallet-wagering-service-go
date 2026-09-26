//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/postgres"
)

// A second Reversal that reaches the compensation indexes (the Wallet lock
// normally prevents it) lost a race: Insert reports ErrCompensationConflict,
// which wraps ErrDuplicateTransaction so ProcessWagerTransaction retries and
// answers REFERENCE_ALREADY_COMPENSATED instead of recording a FAILED
// operation.
func TestInsertMapsCompensationIndexesToCompensationConflict(t *testing.T) {
	s := newSchema(t)
	ctx := context.Background()
	repo := postgres.NewTransactionRepository(s.app)

	betID := uuid.Must(uuid.NewV7())
	s.mustExec(t, s.owner, insertExternal, betID, s.walletID, s.playerID, "BET", "PROCESSED", "provider-a", "bet-1", "key-bet-1", nil, nil)
	refundID := uuid.Must(uuid.NewV7())
	s.mustExec(t, s.owner, insertExternal, refundID, s.walletID, s.playerID, "REFUND", "PROCESSED", "provider-a", "refund-1", "key-refund-1", betID, betID)
	refund, err := repo.Get(ctx, refundID)
	if err != nil {
		t.Fatalf("get refund: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*wager.Snapshot)
	}{
		{"compensated BET", func(sn *wager.Snapshot) { sn.Kind = wager.KindRollback }},
		{"processed reversal of the same reference", func(sn *wager.Snapshot) {
			sn.Kind = wager.KindRollback
			sn.CompensatedBetID = uuid.Nil
		}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sn := refund.Snapshot()
			sn.ID = uuid.Must(uuid.NewV7())
			sn.ExternalTransactionID = "second-" + string(rune('a'+i))
			sn.IdempotencyKey = "key-second-" + string(rune('a'+i))
			tt.mutate(&sn)
			second, err := wager.Rehydrate(sn)
			if err != nil {
				t.Fatalf("rehydrate: %v", err)
			}
			err = repo.Insert(ctx, second)
			if !errors.Is(err, app.ErrCompensationConflict) || !errors.Is(err, app.ErrDuplicateTransaction) {
				t.Fatalf("Insert = %v, want ErrCompensationConflict (a duplicate)", err)
			}
		})
	}
}
