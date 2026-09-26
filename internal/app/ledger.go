package app

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
)

// Ledger page sizes.
const (
	DefaultLedgerLimit = 50
	MaxLedgerLimit     = 200
)

// LedgerPosition is a place in the stable (createdAt, id) order of a
// Wallet's Ledger Entries.
type LedgerPosition struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// cursorLen is 8 bytes of Unix microseconds plus the 16-byte entry id.
const cursorLen = 8 + 16

// EncodeLedgerCursor returns the opaque base64url cursor of p.
func EncodeLedgerCursor(p LedgerPosition) string {
	b := make([]byte, cursorLen)
	binary.BigEndian.PutUint64(b, uint64(p.CreatedAt.UnixMicro()))
	copy(b[8:], p.ID[:])
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeLedgerCursor parses a cursor from EncodeLedgerCursor; anything else
// fails with ErrInvalidCommand.
func DecodeLedgerCursor(s string) (LedgerPosition, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != cursorLen {
		return LedgerPosition{}, fmt.Errorf("%w: cursor is invalid", ErrInvalidCommand)
	}
	var id uuid.UUID
	copy(id[:], b[8:])
	return LedgerPosition{
		CreatedAt: time.UnixMicro(int64(binary.BigEndian.Uint64(b))).UTC(),
		ID:        id,
	}, nil
}

// ListLedgerQuery asks for one page of a Wallet's ledger. Cursor is empty
// for the first page; Limit 0 means DefaultLedgerLimit.
type ListLedgerQuery struct {
	WalletID uuid.UUID
	Cursor   string
	Limit    int
}

// LedgerPage is one page of Ledger Entries in (createdAt, id) order.
// NextCursor is empty on the last page.
type LedgerPage struct {
	Entries    []ledger.Entry
	NextCursor string
}

// ListLedger pages through a Wallet's Ledger Entries with a keyset cursor,
// so pages stay stable while new entries are appended.
type ListLedger struct {
	wallets WalletRepository
	ledger  LedgerRepository
}

// NewListLedger builds the query over non-transactional repositories.
func NewListLedger(wallets WalletRepository, ledger LedgerRepository) *ListLedger {
	return &ListLedger{wallets: wallets, ledger: ledger}
}

// Execute returns the page. Errors: ErrInvalidCommand (limit outside
// 1..MaxLedgerLimit, invalid cursor), ErrWalletNotFound, ErrTransient.
func (q *ListLedger) Execute(ctx context.Context, query ListLedgerQuery) (LedgerPage, error) {
	limit := query.Limit
	if limit == 0 {
		limit = DefaultLedgerLimit
	}
	if limit < 1 || limit > MaxLedgerLimit {
		return LedgerPage{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidCommand, MaxLedgerLimit)
	}
	var after *LedgerPosition
	if query.Cursor != "" {
		p, err := DecodeLedgerCursor(query.Cursor)
		if err != nil {
			return LedgerPage{}, err
		}
		after = &p
	}
	if _, err := q.wallets.Get(ctx, query.WalletID); err != nil {
		return LedgerPage{}, err
	}

	// One extra row tells whether another page follows.
	entries, err := q.ledger.List(ctx, query.WalletID, after, limit+1)
	if err != nil {
		return LedgerPage{}, err
	}
	page := LedgerPage{Entries: entries}
	if len(entries) > limit {
		page.Entries = entries[:limit]
		last := page.Entries[limit-1]
		page.NextCursor = EncodeLedgerCursor(LedgerPosition{CreatedAt: last.CreatedAt(), ID: last.ID()})
	}
	return page, nil
}
