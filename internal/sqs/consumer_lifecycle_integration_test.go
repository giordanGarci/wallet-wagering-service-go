//go:build integration

package sqs_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

func TestTransientDatabaseFailureIsRetriedWithBackoff(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "100.00")
	release := e.lockWallet(t, w.ID)
	m := envelopeOf(op(w, "BET", "10.00"))
	e.sendEnvelope(t, m)

	// The connection dies while the operation waits for the Wallet lock.
	e.waitBlocked(t)
	if n := e.terminateBlocked(t); n == 0 {
		t.Fatal("no backend terminated")
	}
	eventually(t, 10*time.Second, "retry recorded", func() bool {
		return metric(t, e.app, "wallet_sqs_retries_total") >= 1
	})
	if n := e.count(t, "SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1", m.Data.ExternalTransactionID); n != 0 {
		t.Fatalf("a transient failure left %d rows", n)
	}
	release()

	r := e.waitRow(t, m.Data.ExternalTransactionID, 15*time.Second)
	if r.Status != "PROCESSED" {
		t.Errorf("status = %s, want PROCESSED", r.Status)
	}
	e.waitQueueEmpty(t, 10*time.Second)
	if v := metric(t, e.app, "wallet_sqs_messages_received_total"); v < 2 {
		t.Errorf("received = %v, want the redelivery counted", v)
	}
	if n := e.ledgerCount(t, m.Data.ExternalTransactionID); n != 1 {
		t.Errorf("ledger entries = %d, want 1", n)
	}
	e.assertReconciled(t, w.ID)
}

func TestPersistentTransientFailureIsRedrivenAfterFiveReceives(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "100.00")
	e.lockWallet(t, w.ID)
	m := envelopeOf(op(w, "BET", "10.00"))
	e.sendEnvelope(t, m)

	// Kill every attempt until the redrive policy (maxReceiveCount=5) moves
	// the message to the DLQ.
	// The goroutine never calls t.Fatal and is always stopped and awaited,
	// even when the test fails.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				_, _ = e.db.Exec(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
					WHERE datname = current_database() AND pid <> pg_backend_pid()
					  AND usename = 'wallet_app' AND wait_event_type = 'Lock'`)
			}
		}
	}()
	stopKiller := func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
		<-done
	}
	defer stopKiller()
	got := e.dlqMessages(t, 1, 45*time.Second)[0]
	stopKiller()

	if attr(got, "dlqReason") != "" {
		t.Errorf("dlqReason = %q, want none (redriven by SQS, not sent by the consumer)", attr(got, "dlqReason"))
	}
	if c, _ := strconv.Atoi(got.Attributes["ApproximateReceiveCount"]); c < 5 {
		t.Errorf("ApproximateReceiveCount = %d, want at least 5", c)
	}
	if v := metric(t, e.app, "wallet_sqs_retries_total"); v < 5 {
		t.Errorf("retries = %v, want 5", v)
	}
	if n := e.count(t, "SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1", m.Data.ExternalTransactionID); n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
}

func TestShutdownWaitsForTheMessageInFlight(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "100.00")
	release := e.lockWallet(t, w.ID)
	m := envelopeOf(op(w, "BET", "10.00"))
	e.sendEnvelope(t, m)
	e.waitBlocked(t)

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		e.app.Stop()
	}()
	time.Sleep(500 * time.Millisecond)
	select {
	case <-stopped:
		t.Fatal("the app stopped without waiting for the message in flight")
	default:
	}
	release()
	<-stopped

	if r := e.waitRow(t, m.Data.ExternalTransactionID, time.Second); r.Status != "PROCESSED" {
		t.Errorf("status = %s, want PROCESSED", r.Status)
	}
	if in, ok := e.inbox(t, m.MessageID); !ok || !in.Completed {
		t.Error("inbox message not completed")
	}
	e.waitQueueEmpty(t, 5*time.Second)
}

func TestShutdownReleasesUnfinishedMessagesForAnotherConsumer(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.ShutdownTimeout = 3 * time.Second })
	w := e.openWallet(t, "100.00")
	release := e.lockWallet(t, w.ID)
	m := envelopeOf(op(w, "BET", "10.00"))
	e.sendEnvelope(t, m)
	e.waitBlocked(t)

	started := time.Now()
	e.app.Stop() // fails the test if the stop hook errs
	if d := time.Since(started); d > 3*time.Second {
		t.Errorf("stop took %s, want within SHUTDOWN_TIMEOUT", d)
	}
	// Visible again at once, instead of after the 30s visibility timeout.
	eventually(t, 3*time.Second, "message visible again", func() bool {
		v, _ := e.queueDepth(t)
		return v == 1
	})
	if n := e.count(t, "SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1", m.Data.ExternalTransactionID); n != 0 {
		t.Fatalf("rows = %d, want 0 (the canceled operation rolled back)", n)
	}
	release()

	next := testsupport.StartApp(t, e.cfg)
	if r := e.waitRow(t, m.Data.ExternalTransactionID, 10*time.Second); r.Status != "PROCESSED" {
		t.Errorf("status = %s, want PROCESSED", r.Status)
	}
	e.waitQueueEmpty(t, 5*time.Second)
	if v := metric(t, next, `wallet_sqs_messages_handled_total{result="processed"}`); v != 1 {
		t.Errorf("next consumer processed = %v, want 1", v)
	}
	if n := e.ledgerCount(t, m.Data.ExternalTransactionID); n != 1 {
		t.Errorf("ledger entries = %d, want 1", n)
	}
}

func TestHeartbeatKeepsLongProcessingInvisible(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Consumer.VisibilityTimeout = 2 * time.Second })
	w := e.openWallet(t, "100.00")
	release := e.lockWallet(t, w.ID)
	m := envelopeOf(op(w, "BET", "10.00"))
	e.sendEnvelope(t, m)
	e.waitBlocked(t)

	// Three visibility timeouts pass while the message is held.
	time.Sleep(6 * time.Second)
	release()
	e.waitRow(t, m.Data.ExternalTransactionID, 10*time.Second)
	e.waitQueueEmpty(t, 5*time.Second)

	if v := metric(t, e.app, "wallet_sqs_messages_received_total"); v != 1 {
		t.Errorf("received = %v, want 1 (no redelivery while processing)", v)
	}
	if v := metric(t, e.app, "wallet_sqs_visibility_extensions_total"); v < 3 {
		t.Errorf("visibility extensions = %v, want at least 3", v)
	}
	if n := e.ledgerCount(t, m.Data.ExternalTransactionID); n != 1 {
		t.Errorf("ledger entries = %d, want 1", n)
	}
}
