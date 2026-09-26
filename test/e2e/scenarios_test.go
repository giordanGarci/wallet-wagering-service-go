//go:build e2e

package e2e_test

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

// sendAll posts every body concurrently (at most parallel in flight), each
// to the next instance round-robin, and returns the replies in input order.
func (c *Cluster) sendAll(t *testing.T, bodies []wager, parallel int) []reply {
	t.Helper()
	token := testsupport.Token(t, testsupport.ProviderAClient)
	out := make([]reply, len(bodies))
	errs := make([]error, len(bodies))
	sem := make(chan struct{}, parallel)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, b := range bodies {
		inst := c.Next()
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i], errs[i] = c.PostWager(inst, token, b)
		}()
	}
	close(start)
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	return out
}

// Scenario 1: the same BET sent 50 times in parallel across the instances
// debits once; the other 49 answers are replays of the same transaction.
func TestSameBetFiftyTimesAcrossInstances(t *testing.T) {
	c := NewCluster(t, nil)
	w := c.OpenWallet(t, c.Next(), "100.00")
	b := bet(w, "10.00")

	bodies := make([]wager, 50)
	for i := range bodies {
		bodies[i] = b
	}
	replies := c.sendAll(t, bodies, 50)

	var fresh, replays int
	ids := map[string]bool{}
	for _, r := range replies {
		if r.Status != http.StatusOK {
			t.Fatalf("duplicate BET = %d %s, want 200", r.Status, r.Body)
		}
		got := decode[wagerResponse](t, r.Body)
		ids[got.TransactionID] = true
		if got.Status != "PROCESSED" || got.Balance == nil || got.Balance.Amount != "90.00" {
			t.Errorf("answer %+v, want PROCESSED with balance 90.00", got)
		}
		if got.IdempotentReplay {
			replays++
		} else {
			fresh++
		}
	}
	if fresh != 1 || replays != 49 || len(ids) != 1 {
		t.Fatalf("fresh %d, replays %d, transaction ids %d; want 1, 49, 1", fresh, replays, len(ids))
	}
	if n := c.Debits(t, w.ID); n != 1 {
		t.Fatalf("debits = %d, want 1", n)
	}
	if got := c.Balance(t, w.ID); got != 9000 {
		t.Fatalf("balance = %d, want 9000", got)
	}
	c.RequireConsistent(t, w.ID)
	c.RequireEventsDelivered(t)
}

// Scenario 2: two BETs of 80.00 on 100.00 sent at once to different
// instances: one PROCESSED, one INSUFFICIENT_FUNDS; resends keep results.
func TestTwoBetsRaceForTheSameFundsAcrossInstances(t *testing.T) {
	c := NewCluster(t, nil)
	w := c.OpenWallet(t, c.Next(), "100.00")
	bets := []wager{bet(w, "80.00"), bet(w, "80.00")}
	replies := c.sendAll(t, bets, 2)

	var winner, loser = -1, -1
	for i, r := range replies {
		got := decode[wagerResponse](t, r.Body)
		switch {
		case r.Status == http.StatusOK && got.Status == "PROCESSED":
			winner = i
		case r.Status == http.StatusUnprocessableEntity && got.FailureCode == "INSUFFICIENT_FUNDS":
			loser = i
		default:
			t.Fatalf("BET %d = %d %s", i, r.Status, r.Body)
		}
	}
	if winner < 0 || loser < 0 {
		t.Fatalf("want one PROCESSED and one INSUFFICIENT_FUNDS, got %v", replies)
	}
	if got := c.Balance(t, w.ID); got != 2000 {
		t.Fatalf("balance = %d, want 2000", got)
	}
	if n := c.Debits(t, w.ID); n != 1 {
		t.Fatalf("debits = %d, want 1", n)
	}

	// Resends, each to every instance, answer the original results.
	for _, inst := range c.Running() {
		r := c.Send(t, inst, bets[winner], http.StatusOK)
		if !r.IdempotentReplay || r.Status != "PROCESSED" {
			t.Errorf("%s: resend of the winner = %+v", inst.Name, r)
		}
		r = c.Send(t, inst, bets[loser], http.StatusUnprocessableEntity)
		if !r.IdempotentReplay || r.FailureCode != "INSUFFICIENT_FUNDS" {
			t.Errorf("%s: resend of the loser = %+v", inst.Name, r)
		}
	}
	if got := c.Balance(t, w.ID); got != 2000 {
		t.Fatalf("balance after resends = %d, want 2000", got)
	}
	c.RequireConsistent(t, w.ID)
	c.RequireEventsDelivered(t)
}

// Scenario 3: distinct wallets processed concurrently across instances.
func TestDistinctWalletsConcurrentlyAcrossInstances(t *testing.T) {
	const wallets, betsPerWallet = 30, 5
	c := NewCluster(t, nil)
	ws := make([]walletBody, wallets)
	for i := range ws {
		ws[i] = c.OpenWallet(t, c.Next(), "100.00")
	}
	var bodies []wager
	for round := range betsPerWallet {
		for _, w := range ws {
			bodies = append(bodies, bet(w, fmt.Sprintf("%d.00", round+1)))
		}
	}
	for i, r := range c.sendAll(t, bodies, 32) {
		if r.Status != http.StatusOK {
			t.Fatalf("BET %d = %d %s, want 200", i, r.Status, r.Body)
		}
	}
	ids := make([]string, len(ws))
	for i, w := range ws {
		ids[i] = w.ID
		// 100.00 - (1+2+3+4+5) = 85.00
		if got := c.Balance(t, w.ID); got != 8500 {
			t.Errorf("wallet %s balance = %d, want 8500", w.ID, got)
		}
		if n := c.Debits(t, w.ID); n != betsPerWallet {
			t.Errorf("wallet %s debits = %d, want %d", w.ID, n, betsPerWallet)
		}
	}
	c.RequireConsistent(t, ids...)
	c.RequireEventsDelivered(t)
}

// Scenario 4a: a REFUND arrives (instance A) before its BET (instance B);
// whichever instance's pending worker picks it up resolves it.
func TestReversalBeforeReferenceResolvedAcrossInstances(t *testing.T) {
	c := NewCluster(t, nil)
	running := c.Running()
	a, b := running[0], running[1]
	w := c.OpenWallet(t, a, "100.00")
	ref := bet(w, "30.00")
	refund := reversalOf(ref, "REFUND")

	if r := c.Send(t, a, refund, http.StatusAccepted); r.Status != "PENDING_REFERENCE" {
		t.Fatalf("early REFUND = %+v, want PENDING_REFERENCE", r)
	}
	if r := c.Send(t, b, ref, http.StatusOK); r.Balance == nil || r.Balance.Amount != "70.00" {
		t.Fatalf("BET = %+v, want balance 70.00", r)
	}
	c.AwaitStatus(t, refund.ExternalTransactionID, "PROCESSED", "", 30*time.Second)

	if got := c.Balance(t, w.ID); got != 10000 {
		t.Fatalf("balance = %d, want 10000 after the refund", got)
	}
	r := c.Send(t, running[2], refund, http.StatusOK)
	if !r.IdempotentReplay || r.Status != "PROCESSED" {
		t.Fatalf("REFUND replay = %+v, want the resolved PROCESSED result", r)
	}
	c.RequireConsistent(t, w.ID)
	byType := c.RequireEventsDelivered(t)
	if byType["WagerTransactionPendingReference"] != 1 {
		t.Errorf("events by type = %v, want one WagerTransactionPendingReference", byType)
	}
}

// Scenario 4b: a REFUND whose Reference never arrives expires with
// REFERENCE_NOT_FOUND after PENDING_MAX_ATTEMPTS (reduced to 3).
func TestReversalBeforeReferenceExpiresAcrossInstances(t *testing.T) {
	c := NewCluster(t, map[string]string{"PENDING_MAX_ATTEMPTS": "3"})
	w := c.OpenWallet(t, c.Next(), "100.00")
	refund := reversalOf(bet(w, "30.00"), "REFUND")

	c.Send(t, c.Next(), refund, http.StatusAccepted)
	c.AwaitStatus(t, refund.ExternalTransactionID, "REJECTED", "REFERENCE_NOT_FOUND", 30*time.Second)

	r := c.Send(t, c.Next(), refund, http.StatusUnprocessableEntity)
	if !r.IdempotentReplay || r.FailureCode != "REFERENCE_NOT_FOUND" {
		t.Fatalf("expired REFUND replay = %+v", r)
	}
	if got := c.Balance(t, w.ID); got != 10000 {
		t.Fatalf("balance = %d, want 10000", got)
	}
	if n := c.Debits(t, w.ID); n != 0 {
		t.Fatalf("debits = %d, want 0", n)
	}
	c.RequireConsistent(t, w.ID)
	c.RequireEventsDelivered(t)
}

// Scenario 5: stopping every instance and starting three new ones keeps
// idempotency (replays), pendings (resolved after the restart) and the
// financial consistency.
func TestRestartOfAllInstancesPreservesState(t *testing.T) {
	c := NewCluster(t, nil)
	w := c.OpenWallet(t, c.Next(), "100.00")
	c.Send(t, c.Next(), bet(w, "10.00"), http.StatusOK)
	processed := bet(w, "10.00")
	first := c.Send(t, c.Next(), processed, http.StatusOK)
	late := bet(w, "25.00")
	refund := reversalOf(late, "REFUND")
	c.Send(t, c.Next(), refund, http.StatusAccepted)

	c.StopAll(t)
	if len(c.Running()) != 0 {
		t.Fatal("instances still running after StopAll")
	}
	c.StartAll(t, Instances, nil)

	for _, inst := range c.Running() {
		r := c.Send(t, inst, processed, http.StatusOK)
		if !r.IdempotentReplay || r.TransactionID != first.TransactionID {
			t.Fatalf("%s: replay after restart = %+v, want replay of %s", inst.Name, r, first.TransactionID)
		}
		r = c.Send(t, inst, refund, http.StatusAccepted)
		if !r.IdempotentReplay || r.Status != "PENDING_REFERENCE" {
			t.Fatalf("%s: pending replay after restart = %+v", inst.Name, r)
		}
	}
	c.Send(t, c.Next(), late, http.StatusOK)
	c.AwaitStatus(t, refund.ExternalTransactionID, "PROCESSED", "", 30*time.Second)

	// 100 - 10 - 10 - 25 + 25
	if got := c.Balance(t, w.ID); got != 8000 {
		t.Fatalf("balance = %d, want 8000", got)
	}
	if n := c.Debits(t, w.ID); n != 3 {
		t.Fatalf("debits = %d, want 3", n)
	}
	c.RequireConsistent(t, w.ID)
	c.RequireEventsDelivered(t)
}
