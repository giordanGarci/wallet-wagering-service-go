package event_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

var (
	now      = time.Date(2026, 9, 8, 12, 0, 0, 123456789, time.UTC)
	playerID = uuid.MustParse("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
	walletID = uuid.MustParse("0192f291-27dd-7d3f-8071-5f8685deef37")
	meta     = event.Meta{CorrelationID: "corr-1", CausationID: "msg-123"}
)

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func walletWith(t *testing.T, balance string) *wallet.Wallet {
	t.Helper()
	w, err := wallet.Rehydrate(walletID, playerID, mustMoney(t, balance, "BRL"), 3, now, now)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func command(t *testing.T, kind, extID, amount, ref string) wager.Command {
	t.Helper()
	cmd, err := wager.NewCommand(wager.CommandInput{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          extID,
		IdempotencyKey:                 "provider-a:" + extID,
		PlayerID:                       playerID.String(),
		WalletID:                       walletID.String(),
		RoundID:                        "round-987",
		GameID:                         "fortune-chimp",
		Kind:                           kind,
		Money:                          mustMoney(t, amount, "BRL"),
		ReferenceExternalTransactionID: ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

func apply(t *testing.T, cmd wager.Command, w *wallet.Wallet, ref wager.Reference) (*wager.Transaction, *ledger.Entry) {
	t.Helper()
	tx, err := wager.NewExternal(cmd, now)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := tx.Apply(w, ref, now)
	if err != nil {
		t.Fatal(err)
	}
	return tx, entry
}

func decode(t *testing.T, e event.Event) map[string]any {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestProcessedBetEmitsProcessedAndBalanceChanged(t *testing.T) {
	w := walletWith(t, "1000.00")
	tx, entry := apply(t, command(t, "BET", "transaction-123", "25.00", ""), w, wager.Reference{})
	events, err := event.ForTransaction(tx, entry, w.Version(), meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type() != event.TypeWagerTransactionProcessed || events[1].Type() != event.TypeWalletBalanceChanged {
		t.Fatalf("events = %+v", events)
	}
	for _, e := range events {
		if e.ID().Version() != 7 || e.AggregateID() != walletID || e.Version() != 1 ||
			e.CorrelationID() != "corr-1" || e.CausationID() != "msg-123" {
			t.Errorf("%s envelope = %+v", e.Type(), e)
		}
		if e.OccurredAt().Location() != time.UTC {
			t.Errorf("%s occurredAt not UTC", e.Type())
		}
	}
	if events[0].ID() == events[1].ID() {
		t.Fatal("event ids must be unique")
	}

	env := decode(t, events[0])
	for _, k := range []string{"eventId", "eventType", "aggregateId", "correlationId", "causationId", "occurredAt", "version", "data"} {
		if _, ok := env[k]; !ok {
			t.Errorf("envelope lacks %q", k)
		}
	}
	if env["occurredAt"] != "2026-09-08T12:00:00.123456Z" || env["version"] != float64(1) || env["eventType"] != "WagerTransactionProcessed" {
		t.Errorf("envelope = %v", env)
	}
	data := env["data"].(map[string]any)
	want := map[string]any{
		"transactionId":         tx.ID().String(),
		"origin":                "EXTERNAL",
		"kind":                  "BET",
		"status":                "PROCESSED",
		"walletId":              walletID.String(),
		"playerId":              playerID.String(),
		"providerId":            "provider-a",
		"externalTransactionId": "transaction-123",
		"roundId":               "round-987",
		"gameId":                "fortune-chimp",
	}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("data.%s = %v, want %v", k, data[k], v)
		}
	}
	if m := data["money"].(map[string]any); m["amount"] != "25.00" || m["currency"] != "BRL" {
		t.Errorf("money = %v", m)
	}
	if b := data["balanceAfter"].(map[string]any); b["amount"] != "975.00" {
		t.Errorf("balanceAfter = %v", b)
	}

	changed := decode(t, events[1])["data"].(map[string]any)
	if changed["walletId"] != walletID.String() || changed["transactionId"] != tx.ID().String() ||
		changed["direction"] != "DEBIT" || changed["walletVersion"] != float64(4) {
		t.Errorf("WalletBalanceChanged data = %v", changed)
	}
	for k, amount := range map[string]string{"money": "25.00", "balanceBefore": "1000.00", "balanceAfter": "975.00"} {
		if m := changed[k].(map[string]any); m["amount"] != amount || m["currency"] != "BRL" {
			t.Errorf("%s = %v, want %s BRL", k, m, amount)
		}
	}
	if len(changed) != 7 {
		t.Errorf("WalletBalanceChanged has %d fields, want 7: %v", len(changed), changed)
	}
}

func TestPayloadIsDeterministic(t *testing.T) {
	w := walletWith(t, "10.00")
	tx, entry := apply(t, command(t, "WIN", "win-1", "5.00", ""), w, wager.Reference{})
	events, err := event.ForTransaction(tx, entry, w.Version(), event.Meta{CorrelationID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		first, _ := json.Marshal(e)
		for i := 0; i < 20; i++ {
			again, _ := json.Marshal(e)
			if !bytes.Equal(first, again) {
				t.Fatal("marshal must be byte-for-byte stable")
			}
		}
		if bytes.Contains(first, []byte("causationId")) {
			t.Error("absent causationId must be omitted")
		}
		if !bytes.HasPrefix(first, []byte(`{"eventId":`)) {
			t.Errorf("envelope field order changed: %s", first)
		}
	}
}

func TestLossEmitsOnlyProcessed(t *testing.T) {
	w := walletWith(t, "10.00")
	tx, entry := apply(t, command(t, "LOSS", "loss-1", "0.00", ""), w, wager.Reference{})
	events, err := event.ForTransaction(tx, entry, w.Version(), meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type() != event.TypeWagerTransactionProcessed {
		t.Fatalf("LOSS events = %v", events)
	}
}

func TestOpeningEventsOmitExternalMetadata(t *testing.T) {
	brl, _ := money.ParseCurrency("BRL")
	w, _ := wallet.New(walletID, playerID, brl, now)
	tx, entry, err := wager.Open(w, mustMoney(t, "1000.00", "BRL"), now)
	if err != nil {
		t.Fatal(err)
	}
	events, err := event.ForTransaction(tx, entry, w.Version(), event.Meta{CorrelationID: "open-1"})
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %v, %v", events, err)
	}
	data := decode(t, events[0])["data"].(map[string]any)
	for _, k := range []string{"providerId", "externalTransactionId", "roundId", "gameId", "referenceExternalTransactionId", "referenceTransactionId"} {
		if _, ok := data[k]; ok {
			t.Errorf("opening event carries %q", k)
		}
	}
	if data["origin"] != "INTERNAL" || data["kind"] != "OPENING" {
		t.Errorf("opening data = %v", data)
	}
	if v := decode(t, events[1])["data"].(map[string]any)["walletVersion"]; v != float64(1) {
		t.Errorf("opening walletVersion = %v, want 1", v)
	}
}

func TestRejectedEventCarriesFailureCode(t *testing.T) {
	w := walletWith(t, "10.00")
	tx, entry := apply(t, command(t, "BET", "bet-1", "80.00", ""), w, wager.Reference{})
	events, err := event.ForTransaction(tx, entry, w.Version(), meta)
	if err != nil || len(events) != 1 || events[0].Type() != event.TypeWagerTransactionRejected {
		t.Fatalf("events = %v, %v", events, err)
	}
	data := decode(t, events[0])["data"].(map[string]any)
	if data["failureCode"] != "INSUFFICIENT_FUNDS" || data["correctable"] != false || data["status"] != "REJECTED" {
		t.Errorf("rejected data = %v", data)
	}
	if b := data["balanceAfter"].(map[string]any); b["amount"] != "10.00" {
		t.Errorf("balanceAfter = %v", b)
	}
	if typed, ok := events[0].Data().(event.WagerTransactionRejectedData); !ok || typed.FailureCode != wager.FailureInsufficientFunds {
		t.Errorf("typed data = %#v", events[0].Data())
	}

	notFound, _ := apply(t, command(t, "BET", "bet-2", "1.00", ""), nil, wager.Reference{})
	e, err := event.NewWagerTransactionRejected(notFound, meta)
	if err != nil {
		t.Fatal(err)
	}
	data = decode(t, e)["data"].(map[string]any)
	if _, ok := data["balanceAfter"]; ok || data["correctable"] != true {
		t.Errorf("WALLET_NOT_FOUND data = %v", data)
	}
}

func TestPendingReferenceEvent(t *testing.T) {
	tx, _ := apply(t, command(t, "REFUND", "refund-1", "25.00", "bet-1"), walletWith(t, "10.00"), wager.Reference{})
	events, err := event.ForTransaction(tx, nil, 0, meta)
	if err != nil || len(events) != 1 || events[0].Type() != event.TypeWagerTransactionPendingReference {
		t.Fatalf("events = %v, %v", events, err)
	}
	data := decode(t, events[0])["data"].(map[string]any)
	if data["referenceExternalTransactionId"] != "bet-1" || data["status"] != "PENDING_REFERENCE" {
		t.Errorf("pending data = %v", data)
	}
}

func TestResolvedReferenceIsPublished(t *testing.T) {
	w := walletWith(t, "100.00")
	bet, _ := apply(t, command(t, "BET", "bet-1", "25.00", ""), w, wager.Reference{})
	refund, entry := apply(t, command(t, "REFUND", "refund-1", "25.00", "bet-1"), w, wager.Reference{Transaction: bet})
	events, err := event.ForTransaction(refund, entry, w.Version(), meta)
	if err != nil {
		t.Fatal(err)
	}
	if data := decode(t, events[0])["data"].(map[string]any); data["referenceTransactionId"] != bet.ID().String() {
		t.Errorf("data = %v", data)
	}
}

func TestConstructorsRejectInvalidInput(t *testing.T) {
	w := walletWith(t, "100.00")
	processed, entry := apply(t, command(t, "BET", "bet-1", "25.00", ""), w, wager.Reference{})
	pending, _ := wager.NewExternal(command(t, "BET", "bet-2", "1.00", ""), now)

	cases := map[string]func() error{
		"processed from pending": func() error { _, err := event.NewWagerTransactionProcessed(pending, meta); return err },
		"rejected from processed": func() error {
			_, err := event.NewWagerTransactionRejected(processed, meta)
			return err
		},
		"pending ref from processed": func() error {
			_, err := event.NewWagerTransactionPendingReference(processed, meta)
			return err
		},
		"nil transaction":        func() error { _, err := event.NewWagerTransactionProcessed(nil, meta); return err },
		"missing correlation id": func() error { _, err := event.NewWagerTransactionProcessed(processed, event.Meta{}); return err },
		"zero entry":             func() error { _, err := event.NewWalletBalanceChanged(ledger.Entry{}, 1, meta); return err },
		"zero wallet version":    func() error { _, err := event.NewWalletBalanceChanged(*entry, 0, meta); return err },
		"entry of another tx": func() error {
			_, err := event.ForTransaction(pending, entry, 2, meta)
			return err
		},
		"nil for transaction": func() error { _, err := event.ForTransaction(nil, nil, 1, meta); return err },
		"marshal zero event":  func() error { _, err := json.Marshal(event.Event{}); return err },
	}
	for name, f := range cases {
		if err := f(); !errors.Is(err, event.ErrInvalidEvent) {
			t.Errorf("%s: error = %v, want ErrInvalidEvent", name, err)
		}
	}
	if events, err := event.ForTransaction(pending, nil, 1, meta); err != nil || len(events) != 0 {
		t.Errorf("PENDING must emit nothing: %v %v", events, err)
	}
}
