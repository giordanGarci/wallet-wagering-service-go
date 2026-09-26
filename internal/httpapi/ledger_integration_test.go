//go:build integration

package httpapi_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

type ledgerItem struct {
	ID            string    `json:"id"`
	TransactionID string    `json:"transactionId"`
	Direction     string    `json:"direction"`
	Money         moneyBody `json:"money"`
	BalanceBefore moneyBody `json:"balanceBefore"`
	BalanceAfter  moneyBody `json:"balanceAfter"`
	CreatedAt     string    `json:"createdAt"`
}

type ledgerPage struct {
	Items      []ledgerItem `json:"items"`
	NextCursor *string      `json:"nextCursor"`
}

// appendCredit inserts, as the schema owner, an external WIN of amount
// minor units on walletID with its credit Ledger Entry at createdAt. It
// stands in for the movements later tickets produce through the API.
func (e *env) appendCredit(t *testing.T, walletID, playerID uuid.UUID, before, amount int64, createdAt time.Time) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	txID := uuid.Must(uuid.NewV7())
	ext := txID.String()
	_, err := e.db.Exec(ctx, `
		INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
			balance_after_minor, balance_after_currency, provider_id, external_transaction_id, idempotency_key,
			payload_hash, round_id, game_id, created_at, updated_at)
		VALUES ($1, 'EXTERNAL', 'WIN', 'PROCESSED', $2, $3, $4, 'BRL', $5, 'BRL', 'provider-a', $6, $6,
			repeat('a', 64), 'round-1', 'game-1', $7, $7)`,
		txID, walletID, playerID, amount, before+amount, ext, createdAt)
	if err != nil {
		t.Fatalf("insert transaction: %v", err)
	}
	_, err = e.db.Exec(ctx, `
		INSERT INTO ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency,
			balance_before_minor, balance_after_minor, created_at)
		VALUES ($1, $2, $3, 'CREDIT', $4, 'BRL', $5, $6, $7)`,
		uuid.Must(uuid.NewV7()), walletID, txID, amount, before, before+amount, createdAt)
	if err != nil {
		t.Fatalf("insert ledger entry: %v", err)
	}
	return txID
}

func (e *env) getLedger(t *testing.T, walletID, query string) (*http.Response, []byte) {
	t.Helper()
	return e.app.Do(t, http.MethodGet, "/wallets/"+walletID+"/ledger"+query,
		testsupport.Token(t, testsupport.InternalClient), "")
}

// expectedLedgerIDs lists the entry ids of walletID in (created_at, id)
// order, read directly from the database.
func (e *env) expectedLedgerIDs(t *testing.T, walletID uuid.UUID) []string {
	t.Helper()
	rows, err := e.db.Query(context.Background(),
		"SELECT id FROM ledger_entries WHERE wallet_id = $1 ORDER BY created_at, id", walletID)
	if err != nil {
		t.Fatalf("query ledger ids: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan id: %v", err)
		}
		ids = append(ids, id.String())
	}
	return ids
}

func TestLedgerListsTheOpeningEntry(t *testing.T) {
	e := newEnv(t)
	playerID := uuid.New()
	w := e.openWalletWith(t, playerID, "250.00")

	resp, body := e.getLedger(t, w.ID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET ledger = %d %s, want 200", resp.StatusCode, body)
	}
	page := decode[ledgerPage](t, body)
	if len(page.Items) != 1 || page.NextCursor != nil {
		t.Fatalf("page = %s, want one item and a null nextCursor", body)
	}
	var txID uuid.UUID
	if err := e.db.QueryRow(context.Background(), "SELECT id FROM wager_transactions WHERE wallet_id = $1", uuid.MustParse(w.ID)).Scan(&txID); err != nil {
		t.Fatalf("read opening: %v", err)
	}
	it := page.Items[0]
	if uuid.MustParse(it.ID).Version() != 7 || it.TransactionID != txID.String() || it.Direction != "CREDIT" ||
		it.Money != (moneyBody{"250.00", "BRL"}) || it.BalanceBefore != (moneyBody{"0.00", "BRL"}) ||
		it.BalanceAfter != (moneyBody{"250.00", "BRL"}) {
		t.Errorf("item = %+v, want the opening credit 0.00 -> 250.00", it)
	}
	if created, err := time.Parse(time.RFC3339Nano, it.CreatedAt); err != nil || time.Since(created) > time.Minute {
		t.Errorf("createdAt = %q, want a recent RFC 3339 timestamp", it.CreatedAt)
	}

	// A zero-balance wallet has an empty ledger.
	empty := e.openWalletWith(t, uuid.New(), "0.00")
	resp, body = e.getLedger(t, empty.ID, "")
	if resp.StatusCode != http.StatusOK || string(body) != "{\"items\":[],\"nextCursor\":null}\n" {
		t.Errorf("empty ledger = %d %s, want 200 with no items", resp.StatusCode, body)
	}
}

func TestLedgerPaginatesInStableOrder(t *testing.T) {
	e := newEnv(t)
	playerID := uuid.New()
	w := e.openWalletWith(t, playerID, "10.00")
	walletID := uuid.MustParse(w.ID)

	base := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
	balance := int64(1000)
	// Two entries share a timestamp: id breaks the tie.
	for _, at := range []time.Time{base, base.Add(time.Millisecond), base.Add(time.Millisecond), base.Add(2 * time.Millisecond), base.Add(3 * time.Millisecond)} {
		e.appendCredit(t, walletID, playerID, balance, 100, at)
		balance += 100
	}
	want := e.expectedLedgerIDs(t, walletID)
	if len(want) != 6 {
		t.Fatalf("ledger has %d entries, want 6", len(want))
	}

	var got []string
	cursor := ""
	for pageNo := 0; ; pageNo++ {
		if pageNo > 5 {
			t.Fatal("pagination does not terminate")
		}
		q := "?limit=2"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		resp, body := e.getLedger(t, w.ID, q)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("page %d = %d %s", pageNo, resp.StatusCode, body)
		}
		page := decode[ledgerPage](t, body)
		if len(page.Items) > 2 {
			t.Fatalf("page %d has %d items, want at most 2", pageNo, len(page.Items))
		}
		for _, it := range page.Items {
			got = append(got, it.ID)
		}
		if pageNo == 0 {
			// An entry appended after the first page shows up at the end,
			// exactly once.
			e.appendCredit(t, walletID, playerID, balance, 100, base.Add(time.Hour))
			want = e.expectedLedgerIDs(t, walletID)
		}
		if page.NextCursor == nil {
			break
		}
		if _, err := base64.RawURLEncoding.DecodeString(*page.NextCursor); err != nil {
			t.Errorf("nextCursor %q is not base64url: %v", *page.NextCursor, err)
		}
		cursor = *page.NextCursor
	}
	if len(got) != len(want) {
		t.Fatalf("paged ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paged ids = %v, want %v (ordered by created_at, id)", got, want)
		}
	}

	// Default limit (50) returns everything in one page; 200 is allowed.
	for _, q := range []string{"", "?limit=200"} {
		resp, body := e.getLedger(t, w.ID, q)
		page := decode[ledgerPage](t, body)
		if resp.StatusCode != http.StatusOK || len(page.Items) != len(want) || page.NextCursor != nil {
			t.Errorf("GET ledger%s = %d with %d items, want 200 with %d and no cursor", q, resp.StatusCode, len(page.Items), len(want))
		}
	}
}

func TestLedgerRejectsInvalidQueriesAndUnknownWallets(t *testing.T) {
	e := newEnv(t)
	w := e.openWalletWith(t, uuid.New(), "1.00")

	for _, q := range []string{
		"?limit=0", "?limit=-1", "?limit=201", "?limit=abc", "?limit=1.5",
		"?cursor=not-base64!", "?cursor=" + base64.RawURLEncoding.EncodeToString([]byte("short")),
	} {
		resp, body := e.getLedger(t, w.ID, q)
		expectError(t, resp, body, http.StatusBadRequest, "INVALID_REQUEST")
	}

	resp, body := e.getLedger(t, uuid.Must(uuid.NewV7()).String(), "")
	expectError(t, resp, body, http.StatusNotFound, "NOT_FOUND")

	resp, body = e.getLedger(t, "not-a-uuid", "")
	expectError(t, resp, body, http.StatusBadRequest, "INVALID_REQUEST")

	resp, body = e.app.Do(t, http.MethodGet, "/wallets/"+w.ID+"/ledger", testsupport.Token(t, testsupport.ProviderAClient), "")
	expectError(t, resp, body, http.StatusForbidden, "FORBIDDEN")
}
