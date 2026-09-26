//go:build e2e && faultinject

package e2e_test

import (
	"bufio"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/crashpoint"
)

// Each scenario starts an api-only instance (it creates the work but never
// consumes, publishes or retries it), then one instance with only the role
// that reaches the crashpoint and CRASH_AT set, alone, so the work can only
// be picked up by it. Once it exited with 137, two healthy instances (every
// role) start and must finish the work exactly once.

const crashTimeout = 60 * time.Second

// crashAt launches an instance with APP_ROLES=role and CRASH_AT=point and
// requires it to exit with 137 at that crashpoint.
func (c *Cluster) crashAt(t *testing.T, role, point string, trigger func()) {
	t.Helper()
	inst := c.Launch(t, map[string]string{"APP_ROLES": role, "CRASH_AT": point})
	if trigger != nil {
		trigger()
	}
	if code := inst.WaitExit(t, crashTimeout); code != crashpoint.ExitCode {
		t.Fatalf("%s exited with %d, want %d:\n%s", inst.Name, code, crashpoint.ExitCode, tail(inst.Logs(), 50))
	}
	if !strings.Contains(inst.Logs(), `"crashpoint":"`+point+`"`) {
		t.Fatalf("%s exited with 137 without the crash log of %s:\n%s", inst.Name, point, tail(inst.Logs(), 50))
	}
}

// metricSum adds series (exact name, no labels) over instances' /metrics.
func (c *Cluster) metricSum(t *testing.T, series string, instances []*Instance) float64 {
	t.Helper()
	var sum float64
	for _, inst := range instances {
		r, err := c.do(inst, http.MethodGet, "/metrics", "", "", nil)
		if err != nil || r.Status != http.StatusOK {
			t.Fatalf("%s: /metrics = %d %v", inst.Name, r.Status, err)
		}
		sc := bufio.NewScanner(strings.NewReader(string(r.Body)))
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), series+" "); ok {
				f, err := strconv.ParseFloat(v, 64)
				if err != nil {
					t.Fatalf("%s: %s = %q", inst.Name, series, v)
				}
				sum += f
			}
		}
	}
	return sum
}

// consumer.after_commit: the BET and its Inbox Message are committed, the
// input message is never deleted. It reappears after the visibility timeout
// and a healthy instance recognizes it through the inbox: one debit.
func TestCrashAfterConsumerCommit(t *testing.T) {
	c := NewStoppedCluster(t, map[string]string{"CONSUMER_VISIBILITY_TIMEOUT": "2s"})
	api := c.Start(t, map[string]string{"APP_ROLES": "api"})
	w := c.OpenWallet(t, api, "100.00")
	b := bet(w, "10.00")
	msgID := "msg-" + uuid.NewString()

	c.crashAt(t, "consumer", crashpoint.ConsumerAfterCommit, func() {
		if err := c.SendSQS(b, msgID); err != nil {
			t.Fatal(err)
		}
	})
	c.AwaitStatus(t, b.ExternalTransactionID, "PROCESSED", "", time.Second)
	if n := c.Count(t, "SELECT count(*) FROM inbox_messages WHERE message_id = $1 AND completed_at IS NOT NULL", msgID); n != 1 {
		t.Fatalf("%d completed inbox messages before the redelivery, want 1", n)
	}

	healthy := c.StartAll(t, 2, nil)
	Eventually(t, 30*time.Second, "redelivery recognized as an inbox duplicate", func() bool {
		return c.metricSum(t, "wallet_inbox_duplicates_total", healthy) == 1
	})

	if n := c.Count(t, "SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1", b.ExternalTransactionID); n != 1 {
		t.Errorf("%d transactions, want 1", n)
	}
	if n := c.Count(t, "SELECT count(*) FROM inbox_messages WHERE message_id = $1", msgID); n != 1 {
		t.Errorf("%d inbox messages, want 1", n)
	}
	if n := c.Debits(t, w.ID); n != 1 {
		t.Errorf("%d debits, want 1", n)
	}
	if got := c.Balance(t, w.ID); got != 9000 {
		t.Errorf("balance = %d, want 9000", got)
	}
	c.RequireConsistent(t, w.ID)
	c.RequireEventsDelivered(t)
}

// outbox.before_publish: the events are committed and leased, nothing was
// sent. After the lease expires a healthy instance publishes every one.
func TestCrashBeforeOutboxPublish(t *testing.T) {
	testOutboxCrash(t, crashpoint.OutboxBeforePublish)
}

// outbox.after_publish: the first event reached SQS, published_at was never
// recorded. After the lease expires a healthy instance sends it again with
// the same eventId (FIFO deduplication drops the copy inside 5 minutes, so
// the queue holds each eventId once) and records it.
func TestCrashAfterOutboxPublish(t *testing.T) {
	testOutboxCrash(t, crashpoint.OutboxAfterPublish)
}

func testOutboxCrash(t *testing.T, point string) {
	c := NewStoppedCluster(t, map[string]string{"OUTBOX_LEASE_DURATION": "2s"})
	api := c.Start(t, map[string]string{"APP_ROLES": "api"})
	w := c.OpenWallet(t, api, "100.00")
	c.Send(t, api, bet(w, "10.00"), http.StatusOK)
	events := c.Count(t, "SELECT count(*) FROM outbox_events")
	if events < 2 {
		t.Fatalf("%d outbox events, want the wallet's and the BET's", events)
	}

	c.crashAt(t, "outbox", point, nil)
	if n := c.Count(t, "SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND locked_until IS NOT NULL"); n != events {
		t.Fatalf("%d of %d events leased and unpublished after the crash", n, events)
	}

	healthy := c.StartAll(t, 2, nil)
	byType := c.RequireEventsDelivered(t)
	// The crashed instance never counted a send (after_publish stops before
	// the counter), so the survivors sent every event, the one already in
	// SQS included.
	if got := c.metricSum(t, "wallet_outbox_published_total", healthy); got != float64(events) {
		t.Errorf("healthy instances published %v events, want %d (events by type %v)", got, events, byType)
	}
	c.RequireConsistent(t, w.ID)
}

// pending.after_claim: a REFUND waiting for its BET is claimed and
// abandoned. After the lease expires, and once the BET is in, a healthy
// instance resolves it: one credit.
func TestCrashAfterPendingClaim(t *testing.T) {
	c := NewStoppedCluster(t, nil)
	api := c.Start(t, map[string]string{"APP_ROLES": "api"})
	w := c.OpenWallet(t, api, "100.00")
	ref := bet(w, "30.00")
	refund := reversalOf(ref, "REFUND")
	c.Send(t, api, refund, http.StatusAccepted)

	c.crashAt(t, "pending", crashpoint.PendingAfterClaim, nil)
	if n := c.Count(t, `SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1
		AND status = 'PENDING_REFERENCE' AND locked_until IS NOT NULL`, refund.ExternalTransactionID); n != 1 {
		t.Fatalf("REFUND not left claimed and pending after the crash")
	}

	if r := c.Send(t, api, ref, http.StatusOK); r.Balance == nil || r.Balance.Amount != "70.00" {
		t.Fatalf("BET = %+v, want balance 70.00", r)
	}
	c.StartAll(t, 2, nil)
	c.AwaitStatus(t, refund.ExternalTransactionID, "PROCESSED", "", 30*time.Second)

	if n := c.Count(t, `SELECT count(*) FROM ledger_entries l JOIN wager_transactions x ON x.id = l.transaction_id
		WHERE x.external_transaction_id = $1 AND l.direction = 'CREDIT'`, refund.ExternalTransactionID); n != 1 {
		t.Errorf("%d REFUND credits, want 1", n)
	}
	if got := c.Balance(t, w.ID); got != 10000 {
		t.Errorf("balance = %d, want 10000", got)
	}
	c.RequireConsistent(t, w.ID)
	c.RequireEventsDelivered(t)
}
