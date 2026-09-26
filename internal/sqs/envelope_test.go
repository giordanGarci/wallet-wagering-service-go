package sqs_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/sqs"
)

// challengeBody is the example of CHALLENGE.md.
const challengeBody = `{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "idempotencyKey": "provider-a:transaction-123",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }
}`

func TestParseEnvelopeChallengeExample(t *testing.T) {
	env, err := sqs.ParseEnvelope([]byte(challengeBody))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.MessageID != "msg-123" || env.CorrelationID != "" || env.OccurredAt.IsZero() {
		t.Errorf("envelope = %+v", env)
	}
	cmd := env.Command
	if cmd.IdempotencyKey() != "provider-a:transaction-123" || cmd.Kind() != wager.KindBet || cmd.ProviderID() != "provider-a" {
		t.Errorf("command = %+v", cmd)
	}
	// Same Payload Hash as the HTTP body with the same fields (see
	// wager.PayloadHash's documented example).
	in := wager.CommandInput{
		ProviderID: "provider-a", ExternalTransactionID: "transaction-123", IdempotencyKey: "other-key",
		PlayerID: "0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1", WalletID: "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID: "round-987", GameID: "fortune-chimp", Kind: "BET", Money: mustMoney(t, "25.00", "BRL"),
	}
	httpCmd, err := wager.NewCommand(in)
	if err != nil {
		t.Fatal(err)
	}
	if wager.PayloadHash(httpCmd) != wager.PayloadHash(cmd) {
		t.Errorf("payload hash differs between HTTP and SQS")
	}
}

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	var m money.Money
	if err := m.UnmarshalJSON([]byte(`{"amount":"` + amount + `","currency":"` + currency + `"}`)); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParseEnvelopeAcceptsVersionAndCorrelation(t *testing.T) {
	body := strings.Replace(challengeBody, `"type"`, `"version": 1, "correlationId": "corr-1", "type"`, 1)
	env, err := sqs.ParseEnvelope([]byte(body))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.CorrelationID != "corr-1" {
		t.Errorf("CorrelationID = %q", env.CorrelationID)
	}
}

func TestParseEnvelopeRejects(t *testing.T) {
	tests := []struct {
		name string
		body string
		want sqs.Reason
	}{
		{"not json", `{"messageId":`, sqs.ReasonMalformedJSON},
		{"array", `[1,2]`, sqs.ReasonMalformedJSON},
		{"string", `"hello"`, sqs.ReasonMalformedJSON},
		{"null", `null`, sqs.ReasonUnknownEnvelope},
		{"unknown type", strings.Replace(challengeBody, "WagerTransactionRequested", "WalletOpened", 1), sqs.ReasonUnknownEnvelope},
		{"missing type", strings.Replace(challengeBody, `"type": "WagerTransactionRequested",`, "", 1), sqs.ReasonUnknownEnvelope},
		{"unknown version", strings.Replace(challengeBody, `"type"`, `"version": 2, "type"`, 1), sqs.ReasonUnknownEnvelope},
		{"unknown envelope field", strings.Replace(challengeBody, `"type"`, `"extra": 1, "type"`, 1), sqs.ReasonInvalidEnvelope},
		{"missing messageId", strings.Replace(challengeBody, `"messageId": "msg-123",`, "", 1), sqs.ReasonInvalidEnvelope},
		{"blank messageId", strings.Replace(challengeBody, `"msg-123"`, `" msg-123"`, 1), sqs.ReasonInvalidEnvelope},
		{"bad occurredAt", strings.Replace(challengeBody, "2026-09-08T12:00:00.000Z", "yesterday", 1), sqs.ReasonInvalidEnvelope},
		{"missing data", `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z"}`, sqs.ReasonInvalidEnvelope},
		{"unknown data field", strings.Replace(challengeBody, `"kind"`, `"extra": 1, "kind"`, 1), sqs.ReasonInvalidCommand},
		{"missing idempotencyKey", strings.Replace(challengeBody, `"idempotencyKey": "provider-a:transaction-123",`, "", 1), sqs.ReasonInvalidCommand},
		{"numeric amount", strings.Replace(challengeBody, `"25.00"`, `25.00`, 1), sqs.ReasonInvalidCommand},
		{"zero BET", strings.Replace(challengeBody, `"25.00"`, `"0.00"`, 1), sqs.ReasonInvalidCommand},
		{"unknown kind", strings.Replace(challengeBody, `"BET"`, `"JACKPOT"`, 1), sqs.ReasonInvalidCommand},
		{"opening", strings.Replace(challengeBody, `"BET"`, `"OPENING"`, 1), sqs.ReasonOpeningKind},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sqs.ParseEnvelope([]byte(tt.body))
			var perm *sqs.PermanentError
			if !errors.As(err, &perm) {
				t.Fatalf("error = %v, want *PermanentError", err)
			}
			if perm.Reason != tt.want {
				t.Errorf("reason = %s (%v), want %s", perm.Reason, err, tt.want)
			}
		})
	}
}

func TestBodyHash(t *testing.T) {
	h := sqs.BodyHash([]byte("abc"))
	if h != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("BodyHash = %s", h)
	}
}
