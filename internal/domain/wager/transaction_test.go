package wager_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestStatusTransitionTable(t *testing.T) {
	all := []wager.Status{wager.StatusPending, wager.StatusPendingReference, wager.StatusProcessed, wager.StatusRejected, wager.StatusFailed}
	allowed := map[wager.Status][]wager.Status{
		wager.StatusPending:          {wager.StatusProcessed, wager.StatusRejected, wager.StatusFailed, wager.StatusPendingReference},
		wager.StatusPendingReference: {wager.StatusProcessed, wager.StatusRejected, wager.StatusFailed},
	}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, a := range allowed[from] {
				want = want || a == to
			}
			if got := from.CanTransitionTo(to); got != want {
				t.Errorf("%s -> %s = %v, want %v", from, to, got, want)
			}
		}
		if from.IsTerminal() != (len(allowed[from]) == 0) {
			t.Errorf("%s IsTerminal = %v", from, from.IsTerminal())
		}
	}
}

func TestParseEnums(t *testing.T) {
	for _, s := range []string{"OPENING", "BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
		if k, err := wager.ParseKind(s); err != nil || k.String() != s {
			t.Errorf("ParseKind(%q) = %v, %v", s, k, err)
		}
	}
	for _, s := range []string{"PENDING", "PENDING_REFERENCE", "PROCESSED", "REJECTED", "FAILED"} {
		if st, err := wager.ParseStatus(s); err != nil || st.String() != s {
			t.Errorf("ParseStatus(%q) = %v, %v", s, st, err)
		}
	}
	for _, s := range []string{"EXTERNAL", "INTERNAL"} {
		if o, err := wager.ParseOrigin(s); err != nil || o.String() != s {
			t.Errorf("ParseOrigin(%q) = %v, %v", s, o, err)
		}
	}
	bad := []func() error{
		func() error { _, err := wager.ParseKind("bet"); return err },
		func() error { _, err := wager.ParseStatus("DONE"); return err },
		func() error { _, err := wager.ParseOrigin(""); return err },
		func() error { _, err := wager.ParseFailureCode("OOPS"); return err },
	}
	for i, f := range bad {
		if err := f(); !errors.Is(err, wager.ErrInvalidTransaction) {
			t.Errorf("bad parse %d error = %v", i, err)
		}
	}
	if wager.KindOpening.IsExternal() || !wager.KindLoss.IsExternal() {
		t.Error("IsExternal classification")
	}
	if !wager.KindRefund.IsReversal() || !wager.KindRollback.IsReversal() || wager.KindWin.IsReversal() {
		t.Error("IsReversal classification")
	}
}

func TestFailureCodeClassification(t *testing.T) {
	cases := []struct {
		code        wager.FailureCode
		correctable bool
	}{
		{wager.FailureInsufficientFunds, false},
		{wager.FailureReversalInsufficientFunds, false},
		{wager.FailureWalletNotFound, true},
		{wager.FailureWalletPlayerMismatch, true},
		{wager.FailureCurrencyMismatch, true},
		{wager.FailureReferenceNotFound, false},
		{wager.FailureReferenceUnsuccessful, false},
		{wager.FailureReferenceMismatch, true},
		{wager.FailureReferenceAmountMismatch, true},
		{wager.FailureReferenceNotReversible, false},
		{wager.FailureReferenceAlreadyCompensated, false},
		{wager.FailurePermanentError, false},
	}
	for _, c := range cases {
		parsed, err := wager.ParseFailureCode(string(c.code))
		if err != nil || parsed != c.code {
			t.Errorf("ParseFailureCode(%s): %v", c.code, err)
		}
		if c.code.Correctable() != c.correctable || c.code.Definitive() == c.correctable {
			t.Errorf("%s: correctable=%v definitive=%v", c.code, c.code.Correctable(), c.code.Definitive())
		}
	}
	unknown := wager.FailureCode("NOPE")
	if unknown.IsValid() || unknown.Correctable() || unknown.Definitive() {
		t.Error("unknown code must be neither correctable nor definitive")
	}
}

func TestNewExternalStartsPendingWithMetadata(t *testing.T) {
	cmd := op(t, wager.KindRefund, "refund-1", "25.00", "bet-1")
	tx := newTx(t, cmd)
	if tx.ID().Version() != 7 || tx.Origin() != wager.OriginExternal || tx.Status() != wager.StatusPending {
		t.Fatalf("id/origin/status = %s %s %s", tx.ID(), tx.Origin(), tx.Status())
	}
	if tx.ProviderID() != "provider-a" || tx.ExternalTransactionID() != "refund-1" || tx.IdempotencyKey() != "provider-a:refund-1" ||
		tx.PayloadHash() != wager.PayloadHash(cmd) || tx.RoundID() != "round-987" || tx.GameID() != "fortune-chimp" ||
		tx.ReferenceExternalTransactionID() != "bet-1" || !tx.HasReference() || tx.Kind() != wager.KindRefund ||
		tx.WalletID() != walletID || tx.PlayerID() != playerID || !tx.Money().Equal(cmd.Money()) {
		t.Fatalf("metadata not copied: %+v", tx.Snapshot())
	}
	if _, ok := tx.BalanceAfter(); ok || tx.FailureCode() != "" || tx.Attempts() != 0 ||
		tx.ReferenceTransactionID() != uuid.Nil || tx.CompensatedBetID() != uuid.Nil {
		t.Fatal("fresh transaction must carry no outcome")
	}
	if !tx.CreatedAt().Equal(t0) || tx.CreatedAt().Location() != time.UTC {
		t.Fatalf("createdAt = %v", tx.CreatedAt())
	}
	if _, err := wager.NewExternal(wager.Command{}, t0); !errors.Is(err, wager.ErrInvalidTransaction) {
		t.Errorf("zero command error = %v", err)
	}
}

func TestNewOpeningIsInternalWithoutExternalMetadata(t *testing.T) {
	tx, err := wager.NewOpening(walletID, playerID, mustMoney(t, "1000.00", "BRL"), t0)
	if err != nil {
		t.Fatal(err)
	}
	s := tx.Snapshot()
	if s.Origin != wager.OriginInternal || s.Kind != wager.KindOpening || s.Status != wager.StatusPending {
		t.Fatalf("opening = %+v", s)
	}
	if s.ProviderID != "" || s.ExternalTransactionID != "" || s.IdempotencyKey != "" || s.PayloadHash != "" ||
		s.RoundID != "" || s.GameID != "" || s.ReferenceExternalTransactionID != "" || tx.HasReference() {
		t.Fatal("opening must not carry external metadata")
	}
	for _, amount := range []string{"0.00", ""} {
		m := money.Money{}
		if amount != "" {
			m = mustMoney(t, amount, "BRL")
		}
		if _, err := wager.NewOpening(walletID, playerID, m, t0); !errors.Is(err, wager.ErrInvalidTransaction) {
			t.Errorf("opening of %q error = %v", amount, err)
		}
	}
	if _, err := wager.NewOpening(uuid.Nil, playerID, mustMoney(t, "1.00", "BRL"), t0); !errors.Is(err, wager.ErrInvalidTransaction) {
		t.Errorf("opening without wallet error = %v", err)
	}
}

func TestTransitionsRecordOutcome(t *testing.T) {
	balance := mustMoney(t, "975.00", "BRL")
	later := t0.Add(time.Second)

	tx := newTx(t, op(t, wager.KindBet, "bet-1", "25.00", ""))
	if err := tx.Process(balance, later); err != nil {
		t.Fatal(err)
	}
	if b, ok := tx.BalanceAfter(); !ok || !b.Equal(balance) || tx.Status() != wager.StatusProcessed || !tx.UpdatedAt().Equal(later) {
		t.Fatal("Process must record status, balance and time")
	}

	tx = newTx(t, op(t, wager.KindBet, "bet-2", "25.00", ""))
	if err := tx.Reject(wager.FailureInsufficientFunds, balance, later); err != nil {
		t.Fatal(err)
	}
	if b, ok := tx.BalanceAfter(); !ok || !b.Equal(balance) || tx.FailureCode() != wager.FailureInsufficientFunds {
		t.Fatal("Reject must record code and balance")
	}

	tx = newTx(t, op(t, wager.KindBet, "bet-3", "25.00", ""))
	if err := tx.Fail(wager.FailurePermanentError, later); err != nil || tx.Status() != wager.StatusFailed {
		t.Fatalf("Fail: %v", err)
	}
}

func TestTerminalStatesAreImmutable(t *testing.T) {
	balance := mustMoney(t, "1.00", "BRL")
	finishers := map[string]func(*wager.Transaction) error{
		"process": func(tx *wager.Transaction) error { return tx.Process(balance, t0) },
		"reject":  func(tx *wager.Transaction) error { return tx.Reject(wager.FailureInsufficientFunds, balance, t0) },
		"fail":    func(tx *wager.Transaction) error { return tx.Fail(wager.FailurePermanentError, t0) },
		"await":   func(tx *wager.Transaction) error { return tx.AwaitReference(t0) },
		"apply": func(tx *wager.Transaction) error {
			_, err := tx.Apply(openWallet(t, "10.00"), wager.Reference{}, t0)
			return err
		},
		"ref miss": func(tx *wager.Transaction) error {
			_, _, err := tx.RecordReferenceMiss(wager.DefaultPendingPolicy(), t0)
			return err
		},
	}
	for first, finish := range map[string]func(*wager.Transaction) error{
		"process": finishers["process"], "reject": finishers["reject"], "fail": finishers["fail"],
	} {
		for second, next := range finishers {
			tx := newTx(t, op(t, wager.KindRollback, "rb-1", "1.00", "bet-1"))
			if err := finish(tx); err != nil {
				t.Fatalf("%s: %v", first, err)
			}
			before := tx.Snapshot()
			if err := next(tx); !errors.Is(err, wager.ErrInvalidTransition) {
				t.Errorf("%s then %s: error = %v, want ErrInvalidTransition", first, second, err)
			}
			if tx.Snapshot() != before {
				t.Errorf("%s then %s mutated a terminal transaction", first, second)
			}
		}
	}
}

func TestAwaitReferenceRules(t *testing.T) {
	tx := newTx(t, op(t, wager.KindBet, "bet-1", "1.00", ""))
	if err := tx.AwaitReference(t0); !errors.Is(err, wager.ErrInvalidTransition) {
		t.Errorf("BET await error = %v", err)
	}
	tx = newTx(t, op(t, wager.KindRefund, "rf-1", "1.00", "bet-1"))
	if err := tx.AwaitReference(t0); err != nil || tx.Status() != wager.StatusPendingReference {
		t.Fatalf("REFUND await: %v", err)
	}
	if err := tx.AwaitReference(t0); !errors.Is(err, wager.ErrInvalidTransition) {
		t.Errorf("PENDING_REFERENCE -> PENDING_REFERENCE error = %v", err)
	}
	if err := tx.Process(mustMoney(t, "1.00", "BRL"), t0); err != nil {
		t.Errorf("PENDING_REFERENCE -> PROCESSED: %v", err)
	}
}

func TestTransitionArgumentsAreValidated(t *testing.T) {
	tx := newTx(t, op(t, wager.KindBet, "bet-1", "1.00", ""))
	neg, _ := mustMoney(t, "1.00", "BRL").Neg()
	checks := map[string]error{
		"process without balance":  tx.Process(money.Money{}, t0),
		"process negative balance": tx.Process(neg, t0),
		"reject unknown code":      tx.Reject("NOPE", money.Money{}, t0),
		"fail unknown code":        tx.Fail("", t0),
	}
	for name, err := range checks {
		if !errors.Is(err, wager.ErrInvalidTransaction) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	if tx.Status() != wager.StatusPending {
		t.Fatal("invalid arguments must not transition")
	}
}

func TestRehydrateRoundTripsAndValidates(t *testing.T) {
	bet := processed(t, op(t, wager.KindBet, "bet-1", "25.00", ""), wager.Reference{})
	back, err := wager.Rehydrate(bet.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if back.Snapshot() != bet.Snapshot() {
		t.Fatal("rehydrate must restore the exact snapshot")
	}
	opening, _, err := wager.Open(freshWallet(t), mustMoney(t, "10.00", "BRL"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wager.Rehydrate(opening.Snapshot()); err != nil {
		t.Fatalf("opening rehydrate: %v", err)
	}

	corrupt := map[string]func(*wager.Snapshot){
		"nil id":                 func(s *wager.Snapshot) { s.ID = uuid.Nil },
		"unknown status":         func(s *wager.Snapshot) { s.Status = "DONE" },
		"unknown origin":         func(s *wager.Snapshot) { s.Origin = "" },
		"external opening":       func(s *wager.Snapshot) { s.Kind = wager.KindOpening },
		"processed without bal":  func(s *wager.Snapshot) { s.BalanceAfter = money.Money{} },
		"processed with code":    func(s *wager.Snapshot) { s.FailureCode = wager.FailureInsufficientFunds },
		"rejected without code":  func(s *wager.Snapshot) { s.Status = wager.StatusRejected },
		"missing provider":       func(s *wager.Snapshot) { s.ProviderID = "" },
		"bad hash":               func(s *wager.Snapshot) { s.PayloadHash = "abc" },
		"upper-case hash":        func(s *wager.Snapshot) { s.PayloadHash = "ABCDEF" + s.PayloadHash[6:] },
		"negative attempts":      func(s *wager.Snapshot) { s.Attempts = -1 },
		"bet with reference":     func(s *wager.Snapshot) { s.ReferenceExternalTransactionID = "x" },
		"bet compensating":       func(s *wager.Snapshot) { s.CompensatedBetID = uuid.Must(uuid.NewV7()) },
		"resolved without ext":   func(s *wager.Snapshot) { s.ReferenceTransactionID = uuid.Must(uuid.NewV7()) },
		"missing timestamps":     func(s *wager.Snapshot) { s.CreatedAt = time.Time{} },
		"uninitialized money":    func(s *wager.Snapshot) { s.Money = money.Money{} },
		"pending ref of the bet": func(s *wager.Snapshot) { s.Status = wager.StatusPendingReference; s.BalanceAfter = money.Money{} },
	}
	for name, edit := range corrupt {
		s := bet.Snapshot()
		edit(&s)
		if _, err := wager.Rehydrate(s); !errors.Is(err, wager.ErrInvalidTransaction) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	s := opening.Snapshot()
	s.ProviderID = "provider-a"
	if _, err := wager.Rehydrate(s); !errors.Is(err, wager.ErrInvalidTransaction) {
		t.Errorf("opening with provider: %v", err)
	}
	s = opening.Snapshot()
	s.Kind = wager.KindBet
	if _, err := wager.Rehydrate(s); !errors.Is(err, wager.ErrInvalidTransaction) {
		t.Errorf("internal BET: %v", err)
	}
}
