//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

type reconciliationBody struct {
	WalletID          string `json:"walletId"`
	StoredBalance     money  `json:"storedBalance"`
	CalculatedBalance money  `json:"calculatedBalance"`
	Difference        money  `json:"difference"`
	Consistent        bool   `json:"consistent"`
	CheckedEntries    int64  `json:"checkedEntries"`
}

// RequireConsistent checks every wallet at the end of a scenario: the
// Reconciliation endpoint (on the next instance) answers consistent, and by
// SQL the stored balance equals credits minus debits, the last Ledger Entry's
// balance_after equals it, and no transaction has two entries.
func (c *Cluster) RequireConsistent(t *testing.T, walletIDs ...string) {
	t.Helper()
	token := testsupport.Token(t, testsupport.InternalClient)
	for _, id := range walletIDs {
		inst := c.Next()
		r, err := c.do(inst, http.MethodPost, "/wallets/"+id+"/reconciliation", token, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != http.StatusOK {
			t.Fatalf("%s: reconcile %s = %d %s", inst.Name, id, r.Status, r.Body)
		}
		rec := decode[reconciliationBody](t, r.Body)
		if !rec.Consistent || rec.StoredBalance != rec.CalculatedBalance || rec.Difference.Amount != "0.00" {
			t.Errorf("reconciliation of %s = %+v, want consistent", id, rec)
		}

		var stored, calculated, lastAfter, dupTx int64
		err = c.DB.QueryRow(context.Background(), `
			SELECT w.balance,
			       COALESCE((SELECT SUM(CASE direction WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END)
			                 FROM ledger_entries WHERE wallet_id = w.id), 0),
			       COALESCE((SELECT balance_after_minor FROM ledger_entries WHERE wallet_id = w.id
			                 ORDER BY created_at DESC, id DESC LIMIT 1), 0),
			       (SELECT count(*) FROM (SELECT transaction_id FROM ledger_entries WHERE wallet_id = w.id
			                 GROUP BY transaction_id HAVING count(*) > 1) d)
			FROM wallets w WHERE w.id = $1`, id).Scan(&stored, &calculated, &lastAfter, &dupTx)
		if err != nil {
			t.Fatalf("sql reconcile %s: %v", id, err)
		}
		if stored != calculated || stored != lastAfter || dupTx != 0 || stored < 0 {
			t.Errorf("wallet %s: stored %d, credits-debits %d, last balance_after %d, transactions with >1 entry %d",
				id, stored, calculated, lastAfter, dupTx)
		}
	}
}

// Balance is the stored balance of walletID in minor units.
func (c *Cluster) Balance(t *testing.T, walletID string) int64 {
	t.Helper()
	var b int64
	if err := c.DB.QueryRow(context.Background(), "SELECT balance FROM wallets WHERE id = $1", walletID).Scan(&b); err != nil {
		t.Fatalf("balance of %s: %v", walletID, err)
	}
	return b
}

// Count runs a SELECT count(*) as the owner.
func (c *Cluster) Count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := c.DB.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// Debits counts the DEBIT Ledger Entries of walletID.
func (c *Cluster) Debits(t *testing.T, walletID string) int {
	t.Helper()
	return c.Count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", walletID)
}

// AwaitStatus polls the Wager Transaction of (provider-a, extID) until it
// has status (and failure code, "" = none) or the timeout expires.
func (c *Cluster) AwaitStatus(t *testing.T, extID, status, failureCode string, timeout time.Duration) {
	t.Helper()
	var got, code string
	Eventually(t, timeout, "transaction "+extID+" reaches "+status+" "+failureCode, func() bool {
		err := c.DB.QueryRow(context.Background(), `
			SELECT status, COALESCE(failure_code, '') FROM wager_transactions
			WHERE provider_id = 'provider-a' AND external_transaction_id = $1`, extID).Scan(&got, &code)
		return err == nil && got == status && code == failureCode
	})
}

// Eventually polls cond every 50ms until it holds or timeout expires.
func Eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for: %s", timeout, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Event is one message read from the events queue.
type Event struct {
	EventID     string `json:"eventId"`
	EventType   string `json:"eventType"`
	AggregateID string `json:"aggregateId"`
	GroupID     string `json:"-"`
	DedupID     string `json:"-"`
}

// RequireEventsDelivered waits until every Outbox Event is published, then
// drains the events queue and checks that each one arrived exactly once,
// with MessageGroupId = aggregateId, MessageDeduplicationId = eventId and,
// per Wallet, in the Outbox order (seq). It returns the events by type.
func (c *Cluster) RequireEventsDelivered(t *testing.T) map[string]int {
	t.Helper()
	ctx := context.Background()
	Eventually(t, 60*time.Second, "every outbox event published", func() bool {
		return c.Count(t, "SELECT count(*) FROM outbox_events WHERE published_at IS NULL") == 0
	})

	// Expected order per aggregate.
	rows, err := c.DB.Query(ctx, "SELECT event_id::text, aggregate_id::text FROM outbox_events ORDER BY seq")
	if err != nil {
		t.Fatalf("outbox rows: %v", err)
	}
	want := map[string][]string{}
	wantIDs := map[string]bool{}
	for rows.Next() {
		var id, agg string
		if err := rows.Scan(&id, &agg); err != nil {
			t.Fatalf("scan outbox: %v", err)
		}
		want[agg] = append(want[agg], id)
		wantIDs[id] = true
	}
	rows.Close()

	got := c.DrainEvents(t, len(wantIDs))
	seen := map[string]int{}
	order := map[string][]string{}
	byType := map[string]int{}
	for _, e := range got {
		seen[e.EventID]++
		order[e.GroupID] = append(order[e.GroupID], e.EventID)
		byType[e.EventType]++
		if e.GroupID != e.AggregateID || e.DedupID != e.EventID {
			t.Errorf("event %s: group %q (aggregate %q), dedup %q", e.EventID, e.GroupID, e.AggregateID, e.DedupID)
		}
		if !wantIDs[e.EventID] {
			t.Errorf("event %s arrived but is not in the outbox", e.EventID)
		}
	}
	for id := range wantIDs {
		if seen[id] != 1 {
			t.Errorf("event %s arrived %d times, want 1", id, seen[id])
		}
	}
	for agg, ids := range want {
		if !equal(order[agg], ids) {
			t.Errorf("wallet %s: events arrived in order %v, want outbox order %v", agg, order[agg], ids)
		}
	}
	return byType
}

// DrainEvents reads (and deletes) messages from the events queue until want
// arrived, then keeps reading for one more second to catch extra ones.
// Several receivers run at once; a FIFO group stays blocked while one of its
// messages is in flight, and each message is recorded before its deletion,
// so the returned order per MessageGroupId is the delivery order.
func (c *Cluster) DrainEvents(t *testing.T, want int) []Event {
	t.Helper()
	const receivers = 4
	q := aws.String(c.Infra.EventsQueueURL)
	var (
		mu    sync.Mutex
		out   []Event
		extra time.Time
		errs  = make([]error, receivers)
		wg    sync.WaitGroup
	)
	deadline := time.Now().Add(60 * time.Second)
	done := func() bool {
		mu.Lock()
		defer mu.Unlock()
		if len(out) < want {
			return time.Now().After(deadline)
		}
		if extra.IsZero() {
			extra = time.Now().Add(time.Second)
		}
		return time.Now().After(extra)
	}
	for r := range receivers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for !done() {
				resp, err := c.Infra.SQS.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
					QueueUrl:                    q,
					MaxNumberOfMessages:         10,
					WaitTimeSeconds:             1,
					MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
				})
				if err != nil {
					errs[r] = fmt.Errorf("receive events: %w", err)
					return
				}
				if len(resp.Messages) == 0 {
					continue
				}
				entries := make([]types.DeleteMessageBatchRequestEntry, 0, len(resp.Messages))
				mu.Lock()
				for i, m := range resp.Messages {
					var e Event
					if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &e); err != nil {
						errs[r] = fmt.Errorf("event body %q: %w", aws.ToString(m.Body), err)
					}
					e.GroupID = m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
					e.DedupID = m.Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)]
					out = append(out, e)
					entries = append(entries, types.DeleteMessageBatchRequestEntry{
						Id: aws.String(strconv.Itoa(i)), ReceiptHandle: m.ReceiptHandle,
					})
				}
				mu.Unlock()
				del, err := c.Infra.SQS.DeleteMessageBatch(ctx, &awssqs.DeleteMessageBatchInput{QueueUrl: q, Entries: entries})
				if err == nil && len(del.Failed) > 0 {
					err = fmt.Errorf("%d deletions failed: %s", len(del.Failed), aws.ToString(del.Failed[0].Message))
				}
				if err != nil {
					errs[r] = fmt.Errorf("delete events: %w", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if len(out) != want {
		t.Fatalf("received %d events, want %d", len(out), want)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
