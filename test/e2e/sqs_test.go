//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

// SendSQS puts b on the input queue as a WagerTransactionRequested envelope
// with messageId (MessageGroupId = walletId, MessageDeduplicationId =
// messageId), idempotencyKey = b.key(). Safe from goroutines.
func (c *Cluster) SendSQS(b wager, messageID string) error {
	data := map[string]any{
		"providerId":            b.ProviderID,
		"externalTransactionId": b.ExternalTransactionID,
		"idempotencyKey":        b.key(),
		"playerId":              b.PlayerID,
		"walletId":              b.WalletID,
		"roundId":               b.RoundID,
		"gameId":                b.GameID,
		"kind":                  b.Kind,
		"money":                 b.Money,
	}
	if b.Reference != "" {
		data["referenceExternalTransactionId"] = b.Reference
	}
	body, err := json.Marshal(map[string]any{
		"messageId":  messageID,
		"type":       "WagerTransactionRequested",
		"version":    1,
		"occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"data":       data,
	})
	if err != nil {
		return err
	}
	_, err = c.Infra.SQS.SendMessage(context.Background(), &awssqs.SendMessageInput{
		QueueUrl:               aws.String(c.Infra.InputQueueURL),
		MessageBody:            aws.String(string(body)),
		MessageGroupId:         aws.String(b.WalletID),
		MessageDeduplicationId: aws.String(messageID),
	})
	return err
}

// Scenario 7: the same operation sent at once over HTTP (to one instance)
// and twice over SQS (distinct messageIds, consumed by any instance) has a
// single financial effect; both Inbox Messages point at that transaction.
func TestSameOperationOverHTTPAndSQSAcrossInstances(t *testing.T) {
	const wallets = 10
	c := NewCluster(t, nil)
	token := testsupport.Token(t, testsupport.ProviderAClient)

	type op struct {
		w      walletBody
		b      wager
		msgIDs [2]string
		http   reply
	}
	ops := make([]*op, wallets)
	for i := range ops {
		w := c.OpenWallet(t, c.Next(), "100.00")
		ops[i] = &op{w: w, b: bet(w, "10.00"), msgIDs: [2]string{"msg-" + uuid.NewString(), "msg-" + uuid.NewString()}}
	}

	errs := make(chan error, wallets*3)
	var wg sync.WaitGroup
	for _, o := range ops {
		inst := c.Next()
		wg.Add(3)
		go func() {
			defer wg.Done()
			var err error
			o.http, err = c.PostWager(inst, token, o.b)
			errs <- err
		}()
		for _, id := range o.msgIDs {
			go func() {
				defer wg.Done()
				errs <- c.SendSQS(o.b, id)
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	Eventually(t, 60*time.Second, "every SQS message completed in the inbox", func() bool {
		return c.Count(t, "SELECT count(*) FROM inbox_messages WHERE completed_at IS NOT NULL") == 2*wallets
	})

	ids := make([]string, 0, wallets)
	for _, o := range ops {
		ids = append(ids, o.w.ID)
		if o.http.Status != http.StatusOK {
			t.Fatalf("HTTP BET = %d %s, want 200", o.http.Status, o.http.Body)
		}
		got := decode[wagerResponse](t, o.http.Body)
		if n := c.Count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = 'provider-a' AND external_transaction_id = $1`, o.b.ExternalTransactionID); n != 1 {
			t.Errorf("wallet %s: %d transactions, want 1", o.w.ID, n)
		}
		if n := c.Count(t, `SELECT count(*) FROM inbox_messages WHERE message_id = ANY($1) AND transaction_id = $2`, o.msgIDs[:], got.TransactionID); n != 2 {
			t.Errorf("wallet %s: %d inbox messages point at %s, want 2", o.w.ID, n, got.TransactionID)
		}
		if n := c.Debits(t, o.w.ID); n != 1 {
			t.Errorf("wallet %s: %d debits, want 1", o.w.ID, n)
		}
		if b := c.Balance(t, o.w.ID); b != 9000 {
			t.Errorf("wallet %s: balance %d, want 9000", o.w.ID, b)
		}
	}
	if n := c.Count(t, "SELECT count(*) FROM wager_transactions WHERE origin = 'EXTERNAL'"); n != wallets {
		t.Errorf("%d external transactions, want %d", n, wallets)
	}
	c.RequireConsistent(t, ids...)
	c.RequireEventsDelivered(t)
}
