package wager_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

func freshWallet(t *testing.T) *wallet.Wallet {
	t.Helper()
	brl, _ := money.ParseCurrency("BRL")
	w, err := wallet.New(walletID, playerID, brl, t0)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestApplyMovesTheWalletPerKind(t *testing.T) {
	cases := []struct {
		kind    wager.Kind
		amount  string
		dir     ledger.Direction // "" = no entry
		balance string
	}{
		{wager.KindBet, "25.00", ledger.Debit, "75.00"},
		{wager.KindWin, "40.00", ledger.Credit, "140.00"},
		{wager.KindLoss, "", "", "100.00"},
	}
	for _, c := range cases {
		w := openWallet(t, "100.00")
		tx := newTx(t, op(t, c.kind, "op-1", c.amount, ""))
		entry, err := tx.Apply(w, wager.Reference{}, t0)
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if tx.Status() != wager.StatusProcessed {
			t.Fatalf("%s: status %s (%s)", c.kind, tx.Status(), tx.FailureCode())
		}
		if b, _ := tx.BalanceAfter(); b.Amount() != c.balance || w.Balance().Amount() != c.balance {
			t.Errorf("%s: balance tx=%v wallet=%v, want %s", c.kind, b, w.Balance(), c.balance)
		}
		if c.dir == "" {
			if entry != nil || w.Version() != 5 {
				t.Errorf("%s: must not create an entry nor bump version (v%d)", c.kind, w.Version())
			}
			continue
		}
		if entry == nil || entry.Direction() != c.dir || entry.TransactionID() != tx.ID() || w.Version() != 6 {
			t.Errorf("%s: entry %+v, version %d", c.kind, entry, w.Version())
		}
	}
}

func TestApplyWalletChecks(t *testing.T) {
	otherPlayer, _ := wallet.Rehydrate(walletID, uuid.Must(uuid.NewV7()), mustMoney(t, "100.00", "BRL"), 1, t0, t0)
	usdWallet, _ := wallet.Rehydrate(walletID, playerID, mustMoney(t, "100.00", "USD"), 1, t0, t0)
	cases := []struct {
		name        string
		w           *wallet.Wallet
		cmd         wager.Command
		code        wager.FailureCode
		wantBalance string // "" = none reported
	}{
		{"wallet not found", nil, op(t, wager.KindBet, "b", "1.00", ""), wager.FailureWalletNotFound, ""},
		{"player mismatch", otherPlayer, op(t, wager.KindBet, "b", "1.00", ""), wager.FailureWalletPlayerMismatch, ""},
		{"currency mismatch", usdWallet, op(t, wager.KindBet, "b", "1.00", ""), wager.FailureCurrencyMismatch, "100.00"},
		{"loss currency mismatch", usdWallet, op(t, wager.KindLoss, "l", "", ""), wager.FailureCurrencyMismatch, "100.00"},
		{"bet beyond balance", openWallet(t, "100.00"), op(t, wager.KindBet, "b", "100.01", ""), wager.FailureInsufficientFunds, "100.00"},
		{"reversal before wallet check", nil, op(t, wager.KindRefund, "r", "1.00", "bet-1"), wager.FailureWalletNotFound, ""},
	}
	for _, c := range cases {
		tx := newTx(t, c.cmd)
		entry, err := tx.Apply(c.w, wager.Reference{}, t0)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if entry != nil || tx.Status() != wager.StatusRejected || tx.FailureCode() != c.code {
			t.Errorf("%s: status %s code %s, want REJECTED %s", c.name, tx.Status(), tx.FailureCode(), c.code)
		}
		b, ok := tx.BalanceAfter()
		if ok != (c.wantBalance != "") || (ok && b.Amount() != c.wantBalance) {
			t.Errorf("%s: balance %v/%v, want %q", c.name, b, ok, c.wantBalance)
		}
		if c.w != nil && (c.w.Version() != 1 && c.w.Version() != 5) {
			t.Errorf("%s: rejected transaction bumped the wallet version", c.name)
		}
	}
}

func TestApplyRejectsMisuse(t *testing.T) {
	tx := newTx(t, op(t, wager.KindBet, "b", "1.00", ""))
	other, _ := wallet.Rehydrate(uuid.Must(uuid.NewV7()), playerID, mustMoney(t, "10.00", "BRL"), 1, t0, t0)
	if _, err := tx.Apply(other, wager.Reference{}, t0); !errors.Is(err, wager.ErrInvalidTransaction) {
		t.Errorf("wrong wallet error = %v", err)
	}
	opening, _ := wager.NewOpening(walletID, playerID, mustMoney(t, "1.00", "BRL"), t0)
	if _, err := opening.Apply(freshWallet(t), wager.Reference{}, t0); !errors.Is(err, wager.ErrInvalidTransaction) {
		t.Errorf("apply opening error = %v", err)
	}
}

// TestTwoConcurrentBetsSerializedByTheWalletLock mirrors the mandatory
// 80+80 over 100 scenario at the domain level.
func TestTwoBetsOverOneHundred(t *testing.T) {
	w := openWallet(t, "100.00")
	first := newTx(t, op(t, wager.KindBet, "bet-a", "80.00", ""))
	second := newTx(t, op(t, wager.KindBet, "bet-b", "80.00", ""))
	e1, _ := first.Apply(w, wager.Reference{}, t0)
	e2, _ := second.Apply(w, wager.Reference{}, t0)
	if first.Status() != wager.StatusProcessed || e1 == nil {
		t.Fatal("first bet must be processed")
	}
	if second.Status() != wager.StatusRejected || second.FailureCode() != wager.FailureInsufficientFunds || e2 != nil {
		t.Fatal("second bet must be rejected with INSUFFICIENT_FUNDS")
	}
	if w.Balance().Amount() != "20.00" {
		t.Fatalf("balance = %s", w.Balance())
	}
}

// reversalCase describes a Reversal or WIN-with-Reference against a Reference.
type reversalCase struct {
	name string
	// ref builds the Reference Transaction (nil for "not found").
	ref         func(t *testing.T) *wager.Transaction
	compensated bool
	cmd         func(t *testing.T) wager.Command
	balance     string // wallet balance before Apply

	status   wager.Status
	code     wager.FailureCode
	dir      ledger.Direction
	slot     bool // CompensatedBetID must equal the Reference id
	resolved bool // ReferenceTransactionID must equal the Reference id
}

func bet(t *testing.T) *wager.Transaction {
	return processed(t, op(t, wager.KindBet, "bet-1", "25.00", ""), wager.Reference{})
}

func win(t *testing.T) *wager.Transaction {
	return processed(t, op(t, wager.KindWin, "win-1", "25.00", ""), wager.Reference{})
}

func refund(t *testing.T) *wager.Transaction {
	return processed(t, op(t, wager.KindRefund, "refund-1", "25.00", "bet-1"), wager.Reference{Transaction: bet(t)})
}

func rollbackOf(ext string, ref func(*testing.T) *wager.Transaction) func(*testing.T) *wager.Transaction {
	return func(t *testing.T) *wager.Transaction {
		return processed(t, op(t, wager.KindRollback, "rollback-0", "25.00", ext), wager.Reference{Transaction: ref(t)})
	}
}

func loss(t *testing.T) *wager.Transaction {
	return processed(t, op(t, wager.KindLoss, "loss-1", "", ""), wager.Reference{})
}

func cmdOf(kind wager.Kind, amount, ref string) func(*testing.T) wager.Command {
	return func(t *testing.T) wager.Command { return op(t, kind, "new-op", amount, ref) }
}

func cmdWith(edit func(*wager.CommandInput)) func(*testing.T) wager.Command {
	return func(t *testing.T) wager.Command {
		in := input(t)
		in.Kind = "REFUND"
		in.ExternalTransactionID = "new-op"
		in.IdempotencyKey = "k-new-op"
		in.ReferenceExternalTransactionID = "bet-1"
		edit(&in)
		cmd, err := wager.NewCommand(in)
		if err != nil {
			t.Fatal(err)
		}
		return cmd
	}
}

func TestReferenceRules(t *testing.T) {
	cases := []reversalCase{
		// Successful movements (ticket 10) and the slot they consume.
		{name: "REFUND of BET credits and consumes the slot", ref: bet, cmd: cmdOf(wager.KindRefund, "25.00", "bet-1"),
			balance: "100.00", status: wager.StatusProcessed, dir: ledger.Credit, slot: true, resolved: true},
		{name: "ROLLBACK of BET credits and consumes the slot", ref: bet, cmd: cmdOf(wager.KindRollback, "25.00", "bet-1"),
			balance: "100.00", status: wager.StatusProcessed, dir: ledger.Credit, slot: true, resolved: true},
		{name: "ROLLBACK of WIN debits", ref: win, cmd: cmdOf(wager.KindRollback, "25.00", "win-1"),
			balance: "100.00", status: wager.StatusProcessed, dir: ledger.Debit, resolved: true},
		{name: "ROLLBACK of REFUND debits and claims no new slot", ref: refund, cmd: cmdOf(wager.KindRollback, "25.00", "refund-1"),
			balance: "100.00", status: wager.StatusProcessed, dir: ledger.Debit, resolved: true},
		{name: "WIN of BET credits", ref: bet, cmd: cmdOf(wager.KindWin, "300.00", "bet-1"),
			balance: "100.00", status: wager.StatusProcessed, dir: ledger.Credit, resolved: true},
		{name: "WIN of already refunded BET still pays", ref: bet, compensated: true, cmd: cmdOf(wager.KindWin, "1.00", "bet-1"),
			balance: "100.00", status: wager.StatusProcessed, dir: ledger.Credit, resolved: true},

		// Pending Reference (ticket 11).
		{name: "absent reference waits", ref: nil, cmd: cmdOf(wager.KindRefund, "25.00", "bet-1"),
			balance: "100.00", status: wager.StatusPendingReference},
		{name: "WIN with absent reference waits", ref: nil, cmd: cmdOf(wager.KindWin, "5.00", "bet-1"),
			balance: "100.00", status: wager.StatusPendingReference},
		{name: "reference still pending waits", cmd: cmdOf(wager.KindRollback, "25.00", "refund-1"), balance: "100.00",
			ref: func(t *testing.T) *wager.Transaction {
				tx := newTx(t, op(t, wager.KindRefund, "refund-1", "25.00", "bet-1"))
				if err := tx.AwaitReference(t0); err != nil {
					t.Fatal(err)
				}
				return tx
			}, status: wager.StatusPendingReference},

		// Unsuccessful and non-reversible references.
		{name: "rejected reference", cmd: cmdOf(wager.KindRefund, "25.00", "bet-1"), balance: "100.00",
			ref:    func(t *testing.T) *wager.Transaction { return withStatus(t, bet(t), wager.StatusRejected, wager.FailureInsufficientFunds) },
			status: wager.StatusRejected, code: wager.FailureReferenceUnsuccessful, resolved: true},
		{name: "failed reference", cmd: cmdOf(wager.KindRollback, "25.00", "bet-1"), balance: "100.00",
			ref:    func(t *testing.T) *wager.Transaction { return withStatus(t, bet(t), wager.StatusFailed, wager.FailurePermanentError) },
			status: wager.StatusRejected, code: wager.FailureReferenceUnsuccessful, resolved: true},
		{name: "ROLLBACK of ROLLBACK", ref: rollbackOf("bet-1", bet), cmd: cmdOf(wager.KindRollback, "25.00", "rollback-0"),
			balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceNotReversible, resolved: true},
		{name: "REFUND of WIN", ref: win, cmd: cmdOf(wager.KindRefund, "25.00", "win-1"),
			balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceNotReversible, resolved: true},
		{name: "REFUND of REFUND", ref: refund, cmd: cmdOf(wager.KindRefund, "25.00", "refund-1"),
			balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceNotReversible, resolved: true},
		{name: "ROLLBACK of LOSS", ref: loss, cmd: cmdOf(wager.KindRollback, "25.00", "loss-1"),
			balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceNotReversible, resolved: true},
		{name: "REFUND of LOSS", ref: loss, cmd: cmdOf(wager.KindRefund, "25.00", "loss-1"),
			balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceNotReversible, resolved: true},
		{name: "ROLLBACK of a still pending ROLLBACK is rejected at once", cmd: cmdOf(wager.KindRollback, "25.00", "rollback-0"), balance: "100.00",
			ref: func(t *testing.T) *wager.Transaction {
				tx := newTx(t, op(t, wager.KindRollback, "rollback-0", "25.00", "bet-1"))
				_ = tx.AwaitReference(t0)
				return tx
			}, status: wager.StatusRejected, code: wager.FailureReferenceNotReversible, resolved: true},
		{name: "WIN referencing a WIN", ref: win, cmd: cmdOf(wager.KindWin, "5.00", "win-1"),
			balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceMismatch, resolved: true},

		// Mismatches.
		{name: "round mismatch", ref: bet, balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceMismatch, resolved: true,
			cmd: cmdWith(func(in *wager.CommandInput) { in.RoundID = "round-other" })},
		{name: "WIN round mismatch", ref: bet, balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceMismatch, resolved: true,
			cmd: cmdWith(func(in *wager.CommandInput) { in.Kind = "WIN"; in.RoundID = "round-other" })},
		{name: "amount mismatch", ref: bet, balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceAmountMismatch, resolved: true,
			cmd: cmdOf(wager.KindRefund, "24.99", "bet-1")},
		{name: "reference on another wallet", balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceMismatch, resolved: true,
			cmd: cmdOf(wager.KindRefund, "25.00", "bet-1"),
			ref: func(t *testing.T) *wager.Transaction {
				s := bet(t).Snapshot()
				s.WalletID = uuid.Must(uuid.NewV7())
				tx, _ := wager.Rehydrate(s)
				return tx
			}},
		{name: "reference of another player", balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceMismatch, resolved: true,
			cmd: cmdOf(wager.KindRefund, "25.00", "bet-1"),
			ref: func(t *testing.T) *wager.Transaction {
				s := bet(t).Snapshot()
				s.PlayerID = uuid.Must(uuid.NewV7())
				tx, _ := wager.Rehydrate(s)
				return tx
			}},
		{name: "reference in another currency", balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceMismatch, resolved: true,
			cmd: cmdOf(wager.KindRefund, "25.00", "bet-1"),
			ref: func(t *testing.T) *wager.Transaction {
				s := bet(t).Snapshot()
				s.Money = mustMoney(t, "25.00", "USD")
				tx, _ := wager.Rehydrate(s)
				return tx
			}},

		// Compensation Slot (ADR-0002).
		{name: "second REFUND of BET", ref: bet, compensated: true, cmd: cmdOf(wager.KindRefund, "25.00", "bet-1"),
			balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceAlreadyCompensated, resolved: true},
		{name: "ROLLBACK after REFUND of the same BET", ref: bet, compensated: true, cmd: cmdOf(wager.KindRollback, "25.00", "bet-1"),
			balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceAlreadyCompensated, resolved: true},
		{name: "second ROLLBACK of WIN", ref: win, compensated: true, cmd: cmdOf(wager.KindRollback, "25.00", "win-1"),
			balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceAlreadyCompensated, resolved: true},
		{name: "second ROLLBACK of REFUND", ref: refund, compensated: true, cmd: cmdOf(wager.KindRollback, "25.00", "refund-1"),
			balance: "100.00", status: wager.StatusRejected, code: wager.FailureReferenceAlreadyCompensated, resolved: true},

		// Funds.
		{name: "ROLLBACK of WIN beyond balance", ref: win, cmd: cmdOf(wager.KindRollback, "25.00", "win-1"),
			balance: "24.99", status: wager.StatusRejected, code: wager.FailureReversalInsufficientFunds, resolved: true},
		{name: "ROLLBACK of REFUND beyond balance", ref: refund, cmd: cmdOf(wager.KindRollback, "25.00", "refund-1"),
			balance: "0.00", status: wager.StatusRejected, code: wager.FailureReversalInsufficientFunds, resolved: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var refTx *wager.Transaction
			if c.ref != nil {
				refTx = c.ref(t)
			}
			w := openWallet(t, c.balance)
			tx := newTx(t, c.cmd(t))
			entry, err := tx.Apply(w, wager.Reference{Transaction: refTx, Compensated: c.compensated}, t0)
			if err != nil {
				t.Fatal(err)
			}
			if tx.Status() != c.status || tx.FailureCode() != c.code {
				t.Fatalf("status %s code %q, want %s %q", tx.Status(), tx.FailureCode(), c.status, c.code)
			}
			if (entry != nil) != (c.dir != "") || (entry != nil && entry.Direction() != c.dir) {
				t.Fatalf("entry = %+v, want direction %q", entry, c.dir)
			}
			if c.dir == "" && (w.Balance().Amount() != c.balance || w.Version() != 5) {
				t.Errorf("wallet moved without an entry: %s v%d", w.Balance(), w.Version())
			}
			wantSlot := uuid.Nil
			if c.slot {
				wantSlot = refTx.ID()
			}
			if tx.CompensatedBetID() != wantSlot {
				t.Errorf("compensatedBetId = %s, want %s", tx.CompensatedBetID(), wantSlot)
			}
			wantRef := uuid.Nil
			if c.resolved {
				wantRef = refTx.ID()
			}
			if tx.ReferenceTransactionID() != wantRef {
				t.Errorf("referenceTransactionId = %s, want %s", tx.ReferenceTransactionID(), wantRef)
			}
			if _, err := wager.Rehydrate(tx.Snapshot()); err != nil {
				t.Errorf("outcome does not rehydrate: %v", err)
			}
		})
	}
}

func TestResolveReferenceReportsRejectionsAsTypedErrors(t *testing.T) {
	tx := newTx(t, op(t, wager.KindRefund, "rf", "25.00", "bet-1"))
	res, err := wager.ResolveReference(tx, wager.Reference{Transaction: bet(t), Compensated: true})
	if !errors.Is(err, wager.ErrRejected) {
		t.Fatalf("error = %v, want ErrRejected", err)
	}
	var rej *wager.RejectionError
	if !errors.As(err, &rej) || rej.Code != wager.FailureReferenceAlreadyCompensated {
		t.Fatalf("rejection = %+v", rej)
	}
	if code, ok := wager.CodeOf(err); !ok || code != wager.FailureReferenceAlreadyCompensated {
		t.Fatalf("CodeOf = %s, %v", code, ok)
	}
	if res.ReferenceTransactionID == uuid.Nil {
		t.Error("rejected resolution must still carry the resolved reference")
	}
	if _, ok := wager.CodeOf(errors.New("plain")); ok {
		t.Error("CodeOf of a plain error must report false")
	}
}

func TestResolveReferenceWithoutReference(t *testing.T) {
	cases := map[wager.Kind]ledger.Direction{wager.KindBet: ledger.Debit, wager.KindWin: ledger.Credit, wager.KindLoss: ""}
	for kind, dir := range cases {
		res, err := wager.ResolveReference(newTx(t, op(t, kind, "x", "1.00", "")), wager.Reference{})
		if err != nil || res.Direction != dir || res.Await {
			t.Errorf("%s: %+v, %v", kind, res, err)
		}
	}
}

func TestResolveReferenceDetectsLookupBugs(t *testing.T) {
	cases := map[string]func(*wager.Snapshot){
		"other provider":    func(s *wager.Snapshot) { s.ProviderID = "provider-b" },
		"other external id": func(s *wager.Snapshot) { s.ExternalTransactionID = "bet-2" },
	}
	for name, edit := range cases {
		s := bet(t).Snapshot()
		edit(&s)
		ref, _ := wager.Rehydrate(s)
		tx := newTx(t, op(t, wager.KindRefund, "rf", "25.00", "bet-1"))
		if _, err := tx.Apply(openWallet(t, "1.00"), wager.Reference{Transaction: ref}, t0); !errors.Is(err, wager.ErrInvalidReference) {
			t.Errorf("%s: error = %v", name, err)
		}
		if tx.Status() != wager.StatusPending {
			t.Errorf("%s: lookup bug must not record an outcome", name)
		}
	}
}

func TestCompensationSlotSequence(t *testing.T) {
	// REFUND, ROLLBACK of the REFUND, then REFUND again (ADR-0002).
	w := openWallet(t, "100.00")
	betTx := newTx(t, op(t, wager.KindBet, "bet-1", "25.00", ""))
	mustApply(t, betTx, w, wager.Reference{}, wager.StatusProcessed)

	refundTx := newTx(t, op(t, wager.KindRefund, "refund-1", "25.00", "bet-1"))
	mustApply(t, refundTx, w, wager.Reference{Transaction: betTx}, wager.StatusProcessed)
	if refundTx.CompensatedBetID() != betTx.ID() {
		t.Fatal("REFUND must consume the BET slot")
	}

	rollbackTx := newTx(t, op(t, wager.KindRollback, "rollback-1", "25.00", "refund-1"))
	mustApply(t, rollbackTx, w, wager.Reference{Transaction: refundTx}, wager.StatusProcessed)
	if rollbackTx.CompensatedBetID() != uuid.Nil {
		t.Fatal("ROLLBACK of REFUND must not claim a slot")
	}

	// The REFUND row still holds the slot: the application reports it.
	again := newTx(t, op(t, wager.KindRefund, "refund-2", "25.00", "bet-1"))
	mustApply(t, again, w, wager.Reference{Transaction: betTx, Compensated: true}, wager.StatusRejected)
	if again.FailureCode() != wager.FailureReferenceAlreadyCompensated {
		t.Fatalf("code = %s", again.FailureCode())
	}
	if w.Balance().Amount() != "75.00" {
		t.Fatalf("balance = %s, want 75.00 (bet debited once, refund undone)", w.Balance())
	}
}

func mustApply(t *testing.T, tx *wager.Transaction, w *wallet.Wallet, ref wager.Reference, want wager.Status) {
	t.Helper()
	if _, err := tx.Apply(w, ref, t0); err != nil {
		t.Fatal(err)
	}
	if tx.Status() != want {
		t.Fatalf("%s status = %s (%s), want %s", tx.ExternalTransactionID(), tx.Status(), tx.FailureCode(), want)
	}
}

func TestPendingReferenceResolvesLater(t *testing.T) {
	w := openWallet(t, "100.00")
	refundTx := newTx(t, op(t, wager.KindRefund, "refund-1", "25.00", "bet-1"))
	mustApply(t, refundTx, w, wager.Reference{}, wager.StatusPendingReference)

	// Worker retry: still absent, status unchanged.
	if entry, err := refundTx.Apply(w, wager.Reference{}, t0); err != nil || entry != nil {
		t.Fatalf("retry: %v %v", entry, err)
	}
	if refundTx.Status() != wager.StatusPendingReference {
		t.Fatal("retry without reference must keep PENDING_REFERENCE")
	}

	betTx := newTx(t, op(t, wager.KindBet, "bet-1", "25.00", ""))
	mustApply(t, betTx, w, wager.Reference{}, wager.StatusProcessed)
	mustApply(t, refundTx, w, wager.Reference{Transaction: betTx}, wager.StatusProcessed)
	if w.Balance().Amount() != "100.00" || refundTx.CompensatedBetID() != betTx.ID() {
		t.Fatalf("balance %s, slot %s", w.Balance(), refundTx.CompensatedBetID())
	}
}

func TestOpen(t *testing.T) {
	w := freshWallet(t)
	tx, entry, err := wager.Open(w, mustMoney(t, "1000.00", "BRL"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Status() != wager.StatusProcessed || tx.Origin() != wager.OriginInternal || tx.Kind() != wager.KindOpening {
		t.Fatalf("opening = %+v", tx.Snapshot())
	}
	if b, _ := tx.BalanceAfter(); b.Amount() != "1000.00" {
		t.Fatalf("balanceAfter = %v", b)
	}
	if entry == nil || entry.Direction() != ledger.Credit || entry.TransactionID() != tx.ID() || w.Version() != wallet.InitialVersion {
		t.Fatalf("entry %+v version %d", entry, w.Version())
	}

	zeroWallet := freshWallet(t)
	tx, entry, err = wager.Open(zeroWallet, mustMoney(t, "0.00", "BRL"), t0)
	if tx != nil || entry != nil || err != nil {
		t.Fatalf("zero opening = %v %v %v", tx, entry, err)
	}
	if _, _, err := wager.Open(freshWallet(t), mustMoney(t, "5.00", "USD"), t0); !errors.Is(err, wallet.ErrCurrencyMismatch) {
		t.Fatalf("currency mismatch error = %v", err)
	}
	if _, _, err := wager.Open(freshWallet(t), mustMoney(t, "0.00", "USD"), t0); err == nil {
		t.Fatal("zero opening in another currency must fail")
	}
}
