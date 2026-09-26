//go:build integration

package sqs_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/sqs"
)

func TestBetViaQueueIsProcessedAndDeleted(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "100.00")
	m := envelopeOf(op(w, "BET", "25.00"))
	m.CorrelationID = "corr-" + uuid.NewString()
	e.sendEnvelope(t, m)

	r := e.waitRow(t, m.Data.ExternalTransactionID, 15*time.Second)
	if r.Status != "PROCESSED" {
		t.Fatalf("status = %s, want PROCESSED", r.Status)
	}
	if n := e.ledgerCount(t, m.Data.ExternalTransactionID); n != 1 {
		t.Errorf("ledger entries = %d, want 1", n)
	}
	if b := e.balance(t, w.ID); b != 7500 {
		t.Errorf("balance = %d, want 7500", b)
	}
	in, ok := e.inbox(t, m.MessageID)
	if !ok || !in.Completed || in.TransactionID == nil || *in.TransactionID != r.ID || in.BodyHash != sqs.BodyHash([]byte(m.json(t))) {
		t.Errorf("inbox = %+v (found %v), want completed for %s with the body hash", in, ok, r.ID)
	}
	// The events carry the envelope's correlationId and the messageId as
	// causation.
	var corr int
	if err := e.db.QueryRow(context.Background(), `
		SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND correlation_id = $2
		  AND payload::jsonb ->> 'causationId' = $3`, w.ID, m.CorrelationID, m.MessageID).Scan(&corr); err != nil {
		t.Fatal(err)
	}
	if corr == 0 {
		t.Error("no outbox event with the message's correlationId and causationId")
	}
	e.waitQueueEmpty(t, 10*time.Second)
	e.assertReconciled(t, w.ID)
	if v := metric(t, e.app, `wallet_sqs_messages_handled_total{result="processed"}`); v != 1 {
		t.Errorf("handled processed = %v, want 1", v)
	}
}

func TestRedeliveryWithSameMessageIDIsDeduplicated(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "100.00")
	m := envelopeOf(op(w, "BET", "10.00"))
	body := m.json(t)
	// Two deliveries of the same message (another MessageDeduplicationId,
	// as after the 5-minute FIFO window).
	e.send(t, w.ID, body)
	e.send(t, w.ID, body)

	e.waitRow(t, m.Data.ExternalTransactionID, 15*time.Second)
	e.waitQueueEmpty(t, 15*time.Second)
	eventually(t, 5*time.Second, "inbox duplicate counted", func() bool {
		return metric(t, e.app, "wallet_inbox_duplicates_total") == 1
	})
	if n := e.ledgerCount(t, m.Data.ExternalTransactionID); n != 1 {
		t.Errorf("ledger entries = %d, want 1", n)
	}
	if n := e.count(t, "SELECT count(*) FROM inbox_messages"); n != 1 {
		t.Errorf("inbox messages = %d, want 1", n)
	}
	if b := e.balance(t, w.ID); b != 9000 {
		t.Errorf("balance = %d, want 9000", b)
	}
	// The duplicate never reached the use case: no replay was counted.
	if v := metric(t, e.app, `wallet_wager_duplicates_total{outcome="replay"}`); v != 0 {
		t.Errorf("replays = %v, want 0", v)
	}
	e.assertReconciled(t, w.ID)
}

func TestSameMessageIDWithDifferentBodyGoesToDLQ(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "100.00")
	first := envelopeOf(op(w, "BET", "10.00"))
	e.sendEnvelope(t, first)
	e.waitRow(t, first.Data.ExternalTransactionID, 15*time.Second)

	second := envelopeOf(op(w, "BET", "20.00"))
	second.MessageID = first.MessageID
	e.sendEnvelope(t, second)

	got := e.dlqMessages(t, 1, 15*time.Second)[0]
	if aws.ToString(got.Body) != second.json(t) {
		t.Errorf("DLQ body = %s, want the original body", aws.ToString(got.Body))
	}
	if r := attr(got, "dlqReason"); r != string(sqs.ReasonBodyHashMismatch) {
		t.Errorf("dlqReason = %q", r)
	}
	if g := got.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]; g != w.ID {
		t.Errorf("DLQ MessageGroupId = %q, want %s", g, w.ID)
	}
	e.waitQueueEmpty(t, 10*time.Second)
	if n := e.count(t, "SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1", second.Data.ExternalTransactionID); n != 0 {
		t.Errorf("the divergent message was executed (%d rows)", n)
	}
	if b := e.balance(t, w.ID); b != 9000 {
		t.Errorf("balance = %d, want 9000", b)
	}
	if v := metric(t, e.app, `wallet_sqs_dlq_messages_total{reason="body_hash_mismatch"}`); v != 1 {
		t.Errorf("dlq metric = %v, want 1", v)
	}
}

func TestInvalidMessagesGoToDLQWithoutBlockingTheGroup(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "100.00")

	unknownType := envelopeOf(op(w, "BET", "1.00"))
	unknownType.Type = "WalletOpened"
	badFormat := envelopeOf(op(w, "BET", "0.00"))
	opening := envelopeOf(op(w, "OPENING", "1.00"))
	cases := []struct {
		body   string
		reason sqs.Reason
	}{
		{`{"messageId": "broken"`, sqs.ReasonMalformedJSON},
		{unknownType.json(t), sqs.ReasonUnknownEnvelope},
		{badFormat.json(t), sqs.ReasonInvalidCommand},
		{opening.json(t), sqs.ReasonOpeningKind},
	}
	for _, c := range cases {
		e.send(t, w.ID, c.body)
	}
	// A valid message behind them in the same group still goes through.
	valid := envelopeOf(op(w, "BET", "5.00"))
	e.sendEnvelope(t, valid)

	got := e.dlqMessages(t, len(cases), 20*time.Second)
	byBody := map[string]types.Message{}
	for _, m := range got {
		byBody[aws.ToString(m.Body)] = m
	}
	for _, c := range cases {
		m, ok := byBody[c.body]
		if !ok {
			t.Errorf("body %.40q not in the DLQ", c.body)
			continue
		}
		if r := attr(m, "dlqReason"); r != string(c.reason) {
			t.Errorf("body %.40q: dlqReason = %q, want %s", c.body, r, c.reason)
		}
		if attr(m, "dlqDetail") == "" || attr(m, "dlqSourceMessageId") == "" {
			t.Errorf("body %.40q: missing dlqDetail/dlqSourceMessageId", c.body)
		}
	}
	if r := e.waitRow(t, valid.Data.ExternalTransactionID, 15*time.Second); r.Status != "PROCESSED" {
		t.Errorf("valid message status = %s", r.Status)
	}
	e.waitQueueEmpty(t, 10*time.Second)
	if n := e.count(t, "SELECT count(*) FROM wager_transactions WHERE origin = 'EXTERNAL'"); n != 1 {
		t.Errorf("external transactions = %d, want only the valid one", n)
	}
	if n := e.count(t, "SELECT count(*) FROM inbox_messages"); n != 1 {
		t.Errorf("inbox messages = %d, want 1", n)
	}
}

func TestBusinessRejectionsDeleteTheMessage(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "10.00")
	tooMuch := envelopeOf(op(w, "BET", "50.00"))
	unknown := op(wallet{ID: uuid.NewString(), PlayerID: w.PlayerID}, "BET", "1.00")
	noWallet := envelopeOf(unknown)
	e.sendEnvelope(t, tooMuch)
	e.sendEnvelope(t, noWallet)

	for _, c := range []struct {
		m    envelope
		code string
	}{{tooMuch, "INSUFFICIENT_FUNDS"}, {noWallet, "WALLET_NOT_FOUND"}} {
		r := e.waitRow(t, c.m.Data.ExternalTransactionID, 15*time.Second)
		if r.Status != "REJECTED" || r.FailureCode == nil || *r.FailureCode != c.code {
			t.Errorf("%s: row = %+v, want REJECTED %s", c.code, r, c.code)
		}
		eventually(t, 5*time.Second, "inbox of "+c.code, func() bool {
			in, ok := e.inbox(t, c.m.MessageID)
			return ok && in.Completed
		})
	}
	e.waitQueueEmpty(t, 10*time.Second)
	e.assertNoDLQ(t)
	if b := e.balance(t, w.ID); b != 1000 {
		t.Errorf("balance = %d, want 1000", b)
	}
}

func TestPendingReversalCompletesItsInboxMessage(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "10.00")
	refund := op(w, "REFUND", "5.00")
	refund.Reference = "bet-not-yet-seen-" + uuid.NewString()
	m := envelopeOf(refund)
	e.sendEnvelope(t, m)

	r := e.waitRow(t, refund.ExternalTransactionID, 15*time.Second)
	if r.Status != "PENDING_REFERENCE" {
		t.Fatalf("status = %s, want PENDING_REFERENCE", r.Status)
	}
	var due int
	if err := e.db.QueryRow(context.Background(),
		"SELECT count(*) FROM wager_transactions WHERE id = $1 AND next_attempt_at IS NOT NULL", r.ID).Scan(&due); err != nil || due != 1 {
		t.Errorf("pending row not scheduled for the worker (%d, %v)", due, err)
	}
	in, ok := e.inbox(t, m.MessageID)
	if !ok || !in.Completed || in.TransactionID == nil || *in.TransactionID != r.ID {
		t.Errorf("inbox = %+v (found %v), want completed for the pending row", in, ok)
	}
	e.waitQueueEmpty(t, 10*time.Second)
	if v := metric(t, e.app, `wallet_sqs_messages_handled_total{result="pending_reference"}`); v != 1 {
		t.Errorf("handled pending_reference = %v, want 1", v)
	}
}

func TestSameOperationOverHTTPThenSQSIsAReplay(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "100.00")
	o := op(w, "BET", "10.00")
	if code, body := e.postWager(t, o); code != http.StatusOK {
		t.Fatalf("POST = %d %v", code, body)
	}
	m := envelopeOf(o)
	e.sendEnvelope(t, m)
	eventually(t, 15*time.Second, "inbox message", func() bool { _, ok := e.inbox(t, m.MessageID); return ok })

	r := e.waitRow(t, o.ExternalTransactionID, time.Second)
	if in, _ := e.inbox(t, m.MessageID); in.TransactionID == nil || *in.TransactionID != r.ID {
		t.Errorf("inbox transaction = %v, want the HTTP one %s", in.TransactionID, r.ID)
	}
	if n := e.ledgerCount(t, o.ExternalTransactionID); n != 1 {
		t.Errorf("ledger entries = %d, want 1", n)
	}
	if v := metric(t, e.app, `wallet_sqs_messages_handled_total{result="replay"}`); v != 1 {
		t.Errorf("handled replay = %v, want 1", v)
	}
	e.waitQueueEmpty(t, 10*time.Second)
}

func TestIdempotencyConflictsGoToDLQ(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "100.00")
	o := op(w, "BET", "10.00")
	if code, body := e.postWager(t, o); code != http.StatusOK {
		t.Fatalf("POST = %d %v", code, body)
	}
	// Same key, different payload.
	keyConflict := o
	keyConflict.Money = json.RawMessage(`{"amount":"11.00","currency":"BRL"}`)
	// Same external id under another key.
	extConflict := o
	extConflict.IdempotencyKey = "other-key-" + uuid.NewString()
	e.sendEnvelope(t, envelopeOf(keyConflict))
	e.sendEnvelope(t, envelopeOf(extConflict))

	reasons := map[string]bool{}
	for _, m := range e.dlqMessages(t, 2, 15*time.Second) {
		reasons[attr(m, "dlqReason")] = true
	}
	for _, want := range []sqs.Reason{sqs.ReasonIdempotencyKeyConflict, sqs.ReasonExternalTransactionConflict} {
		if !reasons[string(want)] {
			t.Errorf("no DLQ message with reason %s (got %v)", want, reasons)
		}
	}
	if n := e.ledgerCount(t, o.ExternalTransactionID); n != 1 {
		t.Errorf("ledger entries = %d, want 1", n)
	}
	e.waitQueueEmpty(t, 10*time.Second)
}

func TestPermanentFailureIsRecordedAndDeadLettered(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "0.00")
	// A balance at the int64 limit makes any credit overflow: the operation
	// can never succeed.
	if _, err := e.db.Exec(context.Background(), "UPDATE wallets SET balance = 9223372036854775807 WHERE id = $1", w.ID); err != nil {
		t.Fatalf("set balance: %v", err)
	}
	m := envelopeOf(op(w, "WIN", "1.00"))
	e.sendEnvelope(t, m)

	got := e.dlqMessages(t, 1, 15*time.Second)[0]
	if r := attr(got, "dlqReason"); r != string(sqs.ReasonPermanentError) {
		t.Errorf("dlqReason = %q, want %s", r, sqs.ReasonPermanentError)
	}
	if aws.ToString(got.Body) != m.json(t) {
		t.Errorf("DLQ body = %s, want the original body", aws.ToString(got.Body))
	}
	r := e.waitRow(t, m.Data.ExternalTransactionID, 5*time.Second)
	if r.Status != "FAILED" {
		t.Errorf("status = %s, want FAILED (recorded for audit)", r.Status)
	}
	if in, ok := e.inbox(t, m.MessageID); !ok || !in.Completed {
		t.Errorf("inbox = %+v (found %v), want completed", in, ok)
	}
	e.waitQueueEmpty(t, 10*time.Second)

	// A redelivery (e.g. after a failed DLQ send) is an inbox duplicate of a
	// FAILED operation: it is dead-lettered too, never silently dropped.
	e.sendEnvelope(t, m)
	if r := attr(e.dlqMessages(t, 1, 15*time.Second)[0], "dlqReason"); r != string(sqs.ReasonPermanentError) {
		t.Errorf("redelivery dlqReason = %q, want %s", r, sqs.ReasonPermanentError)
	}
	e.waitQueueEmpty(t, 10*time.Second)

	// Another message with the same operation is a Replay of the FAILED
	// record (the path a crash between the FAILED record and its Inbox
	// Message also takes): dead-lettered, and its Inbox Message recorded.
	replay := m
	replay.MessageID = "msg-" + uuid.NewString()
	e.sendEnvelope(t, replay)
	if r := attr(e.dlqMessages(t, 1, 15*time.Second)[0], "dlqReason"); r != string(sqs.ReasonPermanentError) {
		t.Errorf("replay dlqReason = %q, want %s", r, sqs.ReasonPermanentError)
	}
	e.waitQueueEmpty(t, 10*time.Second)
	if in, ok := e.inbox(t, replay.MessageID); !ok || !in.Completed || in.TransactionID == nil || *in.TransactionID != r.ID {
		t.Errorf("replay inbox = %+v (found %v), want completed for %s", in, ok, r.ID)
	}
	// Each dead-lettered message counts once, three messages in all.
	if v := metric(t, e.app, `wallet_sqs_dlq_messages_total{reason="permanent_error"}`); v != 3 {
		t.Errorf("dlq metric = %v, want 3", v)
	}
	if n := e.count(t, "SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1", m.Data.ExternalTransactionID); n != 1 {
		t.Errorf("wager rows = %d, want 1", n)
	}
	if n := e.count(t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1", w.ID); n != 0 {
		t.Errorf("ledger entries = %d, want 0", n)
	}
}

func TestGroupMessagesAreAppliedInOrder(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "100.00")
	var ext []string
	for i := range 6 {
		m := envelopeOf(op(w, "BET", "1.0"+string(rune('0'+i))))
		ext = append(ext, m.Data.ExternalTransactionID)
		e.sendEnvelope(t, m)
	}
	e.waitRow(t, ext[len(ext)-1], 20*time.Second)
	rows, err := e.db.Query(context.Background(), `
		SELECT w.external_transaction_id FROM ledger_entries l JOIN wager_transactions w ON w.id = l.transaction_id
		WHERE l.wallet_id = $1 AND w.origin = 'EXTERNAL' ORDER BY l.created_at, l.id`, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if strings.Join(got, ",") != strings.Join(ext, ",") {
		t.Errorf("ledger order = %v, want send order %v", got, ext)
	}
	e.assertReconciled(t, w.ID)
}

func TestSameOperationConcurrentlyOverHTTPAndSQSHasOneEffect(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "100.00")
	const rounds = 5
	for range rounds {
		o := op(w, "BET", "1.00")
		m := envelopeOf(o)
		var wg sync.WaitGroup
		var code int
		var body map[string]any
		wg.Add(2)
		go func() { defer wg.Done(); e.sendEnvelope(t, m) }()
		go func() { defer wg.Done(); code, body = e.postWager(t, o) }()
		wg.Wait()
		if code != http.StatusOK {
			t.Fatalf("POST = %d %v", code, body)
		}
		eventually(t, 15*time.Second, "SQS side handled", func() bool { _, ok := e.inbox(t, m.MessageID); return ok })
		if n := e.ledgerCount(t, o.ExternalTransactionID); n != 1 {
			t.Errorf("ledger entries = %d, want exactly 1", n)
		}
		if n := e.count(t, "SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1", o.ExternalTransactionID); n != 1 {
			t.Errorf("rows = %d, want 1", n)
		}
	}
	if b := e.balance(t, w.ID); b != 10000-rounds*100 {
		t.Errorf("balance = %d, want %d", b, 10000-rounds*100)
	}
	// Each operation was new on one channel and a replay on the other.
	replays := metric(t, e.app, `wallet_wager_duplicates_total{outcome="replay"}`)
	if replays != rounds {
		t.Errorf("replays = %v, want %d", replays, rounds)
	}
	e.waitQueueEmpty(t, 10*time.Second)
	e.assertReconciled(t, w.ID)
}

// assertNoDLQ checks the DLQ stays empty for a second.
func (e *env) assertNoDLQ(t *testing.T) {
	t.Helper()
	out, err := e.infra.SQS.ReceiveMessage(context.Background(), &awssqs.ReceiveMessageInput{
		QueueUrl:        aws.String(e.infra.DLQQueueURL),
		WaitTimeSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 0 {
		t.Errorf("DLQ holds %d messages, want none", len(out.Messages))
	}
}

func TestSameMessageInTwoGroupsAtOnceHasOneEffect(t *testing.T) {
	e := newEnv(t, nil)
	w := e.openWallet(t, "100.00")
	const rounds = 5
	for range rounds {
		m := envelopeOf(op(w, "BET", "1.00"))
		body := m.json(t)
		// Different groups go to different handlers concurrently, so both
		// copies race for the Inbox Message.
		e.send(t, w.ID, body)
		e.send(t, "other-"+uuid.NewString(), body)
		e.waitRow(t, m.Data.ExternalTransactionID, 15*time.Second)
		e.waitQueueEmpty(t, 15*time.Second)
		if n := e.ledgerCount(t, m.Data.ExternalTransactionID); n != 1 {
			t.Errorf("ledger entries = %d, want 1", n)
		}
	}
	if v := metric(t, e.app, "wallet_inbox_duplicates_total"); v != rounds {
		t.Errorf("inbox duplicates = %v, want %d", v, rounds)
	}
	if b := e.balance(t, w.ID); b != 10000-rounds*100 {
		t.Errorf("balance = %d, want %d", b, 10000-rounds*100)
	}
	e.assertNoDLQ(t)
	e.assertReconciled(t, w.ID)
}
