//go:build integration

package sqs_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

// env is one app running the api and consumer roles against isolated
// infrastructure; db is the schema owner's pool for assertions.
type env struct {
	infra *testsupport.Infra
	cfg   config.Config
	app   *testsupport.App
	db    *pgxpool.Pool
}

// consumerConfig enables api+consumer with short polling and retries.
func consumerConfig(infra *testsupport.Infra) config.Config {
	cfg := infra.Config
	cfg.Roles = []config.Role{config.RoleAPI, config.RoleConsumer}
	cfg.Consumer.WaitTime = time.Second
	cfg.Consumer.VisibilityTimeout = 30 * time.Second
	cfg.Consumer.RetryBase = time.Second
	cfg.Consumer.RetryMax = time.Second
	cfg.Consumer.Concurrency = 4
	return cfg
}

// newEnv starts the app; tweak adjusts the config before start.
func newEnv(t *testing.T, tweak func(*config.Config), extra ...fx.Option) *env {
	t.Helper()
	infra := testsupport.NewInfra(t)
	cfg := consumerConfig(infra)
	if tweak != nil {
		tweak(&cfg)
	}
	e := &env{infra: infra, cfg: cfg, db: testsupport.OpenPool(t, infra.DatabaseURL)}
	e.app = testsupport.StartApp(t, cfg, extra...)
	return e
}

type wallet struct {
	ID       string `json:"id"`
	PlayerID string `json:"playerId"`
}

// openWallet opens a BRL Wallet with amount through the internal API.
func (e *env) openWallet(t *testing.T, amount string) wallet {
	t.Helper()
	body := `{"playerId":"` + uuid.NewString() + `","initialBalance":{"amount":"` + amount + `","currency":"BRL"}}`
	resp, raw := e.app.Do(t, http.MethodPost, "/wallets", testsupport.Token(t, testsupport.InternalClient), body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open wallet = %d %s", resp.StatusCode, raw)
	}
	var w wallet
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

// operation is the data of a WagerTransactionRequested envelope.
type operation struct {
	ProviderID            string          `json:"providerId,omitempty"`
	ExternalTransactionID string          `json:"externalTransactionId,omitempty"`
	IdempotencyKey        string          `json:"idempotencyKey,omitempty"`
	PlayerID              string          `json:"playerId,omitempty"`
	WalletID              string          `json:"walletId,omitempty"`
	RoundID               string          `json:"roundId,omitempty"`
	GameID                string          `json:"gameId,omitempty"`
	Kind                  string          `json:"kind,omitempty"`
	Money                 json.RawMessage `json:"money,omitempty"`
	Reference             string          `json:"referenceExternalTransactionId,omitempty"`
}

func op(w wallet, kind, amount string) operation {
	ext := strings.ToLower(kind) + "-" + uuid.NewString()
	return operation{
		ProviderID:            "provider-a",
		ExternalTransactionID: ext,
		IdempotencyKey:        "provider-a:" + ext,
		PlayerID:              w.PlayerID,
		WalletID:              w.ID,
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  kind,
		Money:                 json.RawMessage(`{"amount":"` + amount + `","currency":"BRL"}`),
	}
}

// httpBody is the HTTP body of o (no idempotencyKey: it is a header).
func (o operation) httpBody(t *testing.T) string {
	t.Helper()
	o.IdempotencyKey = ""
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// envelope wraps o in a WagerTransactionRequested message.
type envelope struct {
	MessageID     string    `json:"messageId"`
	Type          string    `json:"type"`
	OccurredAt    string    `json:"occurredAt"`
	CorrelationID string    `json:"correlationId,omitempty"`
	Data          operation `json:"data"`
}

func envelopeOf(o operation) envelope {
	return envelope{
		MessageID:  "msg-" + uuid.NewString(),
		Type:       "WagerTransactionRequested",
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		Data:       o,
	}
}

func (m envelope) json(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// send puts body on the input queue with MessageGroupId = group and a
// fresh MessageDeduplicationId (so a deliberate resend is not dropped by
// the FIFO deduplication window).
func (e *env) send(t *testing.T, group, body string) {
	t.Helper()
	_, err := e.infra.SQS.SendMessage(context.Background(), &awssqs.SendMessageInput{
		QueueUrl:               aws.String(e.infra.InputQueueURL),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(group),
		MessageDeduplicationId: aws.String(uuid.NewString()),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
}

// sendEnvelope sends m in its Wallet's group.
func (e *env) sendEnvelope(t *testing.T, m envelope) {
	t.Helper()
	e.send(t, m.Data.WalletID, m.json(t))
}

// postWager sends o over HTTP as provider-a with o's Idempotency Key.
func (e *env) postWager(t *testing.T, o operation) (int, map[string]any) {
	t.Helper()
	h := http.Header{
		"Authorization":   {"Bearer " + testsupport.Token(t, testsupport.ProviderAClient)},
		"Idempotency-Key": {o.IdempotencyKey},
	}
	resp, raw := e.app.DoWith(t, http.MethodPost, "/wagering/transactions", o.httpBody(t), h)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body
}

func (e *env) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// row is a wager_transactions row of provider-a.
type row struct {
	ID          string
	Status      string
	FailureCode *string
}

// waitRow waits for provider-a's transaction extID to exist.
func (e *env) waitRow(t *testing.T, extID string, within time.Duration) row {
	t.Helper()
	var r row
	eventually(t, within, "transaction "+extID+" recorded", func() bool {
		err := e.db.QueryRow(context.Background(), `
			SELECT id::text, status, failure_code FROM wager_transactions
			WHERE provider_id = 'provider-a' AND external_transaction_id = $1`, extID).Scan(&r.ID, &r.Status, &r.FailureCode)
		return err == nil
	})
	return r
}

// ledgerCount counts the Ledger Entries of transaction extID.
func (e *env) ledgerCount(t *testing.T, extID string) int {
	t.Helper()
	return e.count(t, `
		SELECT count(*) FROM ledger_entries l JOIN wager_transactions w ON w.id = l.transaction_id
		WHERE w.provider_id = 'provider-a' AND w.external_transaction_id = $1`, extID)
}

func (e *env) balance(t *testing.T, walletID string) int64 {
	t.Helper()
	var b int64
	if err := e.db.QueryRow(context.Background(), "SELECT balance FROM wallets WHERE id = $1", walletID).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

// assertReconciled checks stored balance == credits - debits.
func (e *env) assertReconciled(t *testing.T, walletID string) {
	t.Helper()
	var stored, calculated int64
	err := e.db.QueryRow(context.Background(), `
		SELECT w.balance,
		       COALESCE(SUM(CASE l.direction WHEN 'CREDIT' THEN l.amount_minor ELSE -l.amount_minor END), 0)
		FROM wallets w LEFT JOIN ledger_entries l ON l.wallet_id = w.id
		WHERE w.id = $1 GROUP BY w.balance`, walletID).Scan(&stored, &calculated)
	if err != nil {
		t.Fatalf("reconcile %s: %v", walletID, err)
	}
	if stored != calculated {
		t.Errorf("wallet %s: stored balance %d != ledger %d", walletID, stored, calculated)
	}
}

// inboxRow is the Inbox Message of messageID.
type inboxRow struct {
	BodyHash      string
	Completed     bool
	TransactionID *string
}

func (e *env) inbox(t *testing.T, messageID string) (inboxRow, bool) {
	t.Helper()
	var r inboxRow
	err := e.db.QueryRow(context.Background(), `
		SELECT body_hash, completed_at IS NOT NULL, transaction_id::text FROM inbox_messages
		WHERE consumer_name = $1 AND message_id = $2`, e.cfg.Consumer.Name, messageID).
		Scan(&r.BodyHash, &r.Completed, &r.TransactionID)
	if err != nil {
		return inboxRow{}, false
	}
	return r, true
}

// queueDepth returns visible + in-flight messages of the input queue.
func (e *env) queueDepth(t *testing.T) (visible, inFlight int) {
	t.Helper()
	out, err := e.infra.SQS.GetQueueAttributes(context.Background(), &awssqs.GetQueueAttributesInput{
		QueueUrl: aws.String(e.infra.InputQueueURL),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		t.Fatalf("queue attributes: %v", err)
	}
	visible, _ = strconv.Atoi(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)])
	inFlight, _ = strconv.Atoi(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessagesNotVisible)])
	return visible, inFlight
}

// waitQueueEmpty waits until the input queue holds no message at all.
func (e *env) waitQueueEmpty(t *testing.T, within time.Duration) {
	t.Helper()
	eventually(t, within, "input queue empty", func() bool {
		v, f := e.queueDepth(t)
		return v == 0 && f == 0
	})
}

// dlqMessages waits until the DLQ holds n messages and returns them
// (and deletes them from the DLQ).
func (e *env) dlqMessages(t *testing.T, n int, within time.Duration) []types.Message {
	t.Helper()
	var got []types.Message
	eventually(t, within, strconv.Itoa(n)+" DLQ messages", func() bool {
		out, err := e.infra.SQS.ReceiveMessage(context.Background(), &awssqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(e.infra.DLQQueueURL),
			MaxNumberOfMessages:         10,
			WaitTimeSeconds:             1,
			VisibilityTimeout:           300,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
			MessageAttributeNames:       []string{"All"},
		})
		if err != nil {
			t.Fatalf("receive dlq: %v", err)
		}
		got = append(got, out.Messages...)
		// Delete what was read: an in-flight message would hold back the
		// rest of its FIFO group.
		for _, m := range out.Messages {
			if _, err := e.infra.SQS.DeleteMessage(context.Background(), &awssqs.DeleteMessageInput{
				QueueUrl: aws.String(e.infra.DLQQueueURL), ReceiptHandle: m.ReceiptHandle,
			}); err != nil {
				t.Fatalf("delete dlq message: %v", err)
			}
		}
		return len(got) >= n
	})
	if len(got) != n {
		t.Fatalf("DLQ messages = %d, want %d", len(got), n)
	}
	return got
}

func attr(m types.Message, name string) string {
	if a, ok := m.MessageAttributes[name]; ok {
		return aws.ToString(a.StringValue)
	}
	return ""
}

// metric scrapes one exact series from /metrics of a (0 when absent).
func metric(t *testing.T, a *testsupport.App, series string) float64 {
	t.Helper()
	code, body := a.Get(t, "/metrics")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics = %d", code)
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), series+" "); ok {
			v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	return 0
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", within, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// lockWallet holds walletID's row lock in an owner transaction until the
// returned release is called (idempotent; also on cleanup).
func (e *env) lockWallet(t *testing.T, walletID string) (release func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE", walletID); err != nil {
		t.Fatal(err)
	}
	released := false
	release = func() {
		if !released {
			released = true
			_ = tx.Rollback(ctx)
		}
	}
	t.Cleanup(release)
	return release
}

// waitBlocked waits until an app connection waits on a row lock.
func (e *env) waitBlocked(t *testing.T) {
	t.Helper()
	eventually(t, 15*time.Second, "an app backend waiting on the wallet lock", func() bool {
		return e.count(t, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND usename = 'wallet_app' AND wait_event_type = 'Lock'`) > 0
	})
}

// terminateBlocked kills the app connections waiting on a row lock, as a
// PostgreSQL restart would; it returns how many.
func (e *env) terminateBlocked(t *testing.T) int {
	t.Helper()
	return e.count(t, `SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
		WHERE datname = current_database() AND pid <> pg_backend_pid()
		  AND usename = 'wallet_app' AND wait_event_type = 'Lock'`)
}
