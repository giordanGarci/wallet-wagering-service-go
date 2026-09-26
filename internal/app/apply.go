package app

import (
	"context"
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// applied is an operation run against its locked Wallet (nil when absent),
// ready to be written by persist.
type applied struct {
	t     *wager.Transaction
	w     *wallet.Wallet
	entry *ledger.Entry
	// expectedVersion is the Wallet's version before Apply, for the
	// versioned UPDATE; walletVersion is the version after it, for the
	// events.
	expectedVersion, walletVersion int64
}

// applyTo runs t.Apply against w, keeping the Wallet versions around it.
func applyTo(t *wager.Transaction, w *wallet.Wallet, ref wager.Reference, now time.Time) (applied, error) {
	a := applied{t: t, w: w}
	if w != nil {
		a.expectedVersion = w.Version()
	}
	entry, err := t.Apply(w, ref, now)
	if err != nil {
		return applied{}, err
	}
	a.entry = entry
	if w != nil {
		a.walletVersion = w.Version()
	}
	return a, nil
}

// persist writes a in order: the Wallet (versioned UPDATE; a conflict is
// counted), the Transaction (save), the Ledger Entry and the Outbox Events.
// The events carry the Ledger Entry as stored.
func persist(ctx context.Context, tx Tx, a applied, metrics WagerMetrics, meta event.Meta, save func() error) error {
	if a.entry != nil {
		if err := tx.Wallets().Update(ctx, a.w, a.expectedVersion); err != nil {
			if errors.Is(err, ErrConcurrencyConflict) {
				metrics.ConcurrencyConflict()
			}
			return err
		}
	}
	if err := save(); err != nil {
		return err
	}
	entry := a.entry
	if entry != nil {
		stored, err := tx.Ledger().Append(ctx, *entry)
		if err != nil {
			return err
		}
		entry = &stored
	}
	events, err := event.ForTransaction(a.t, entry, a.walletVersion, meta)
	if err != nil {
		return err
	}
	return tx.Outbox().Append(ctx, events...)
}

// dbTime is t as stored by Postgres: UTC, microsecond precision.
func dbTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}
