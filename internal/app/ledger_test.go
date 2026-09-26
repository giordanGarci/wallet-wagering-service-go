package app_test

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/app"
)

func TestLedgerCursorRoundTrip(t *testing.T) {
	p := app.LedgerPosition{
		CreatedAt: time.Date(2026, 9, 23, 12, 0, 0, 123456000, time.UTC),
		ID:        uuid.Must(uuid.NewV7()),
	}
	cursor := app.EncodeLedgerCursor(p)
	if _, err := base64.RawURLEncoding.DecodeString(cursor); err != nil {
		t.Fatalf("cursor %q is not unpadded base64url: %v", cursor, err)
	}
	got, err := app.DecodeLedgerCursor(cursor)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.CreatedAt.Equal(p.CreatedAt) || got.ID != p.ID {
		t.Errorf("decoded %+v, want %+v", got, p)
	}
}

func TestDecodeLedgerCursorRejectsGarbage(t *testing.T) {
	for _, c := range []string{
		"not base64!",
		base64.RawURLEncoding.EncodeToString([]byte("too short")),
		base64.RawURLEncoding.EncodeToString(make([]byte, 25)),
		base64.StdEncoding.EncodeToString(make([]byte, 24)) + "=",
	} {
		if _, err := app.DecodeLedgerCursor(c); !errors.Is(err, app.ErrInvalidCommand) {
			t.Errorf("DecodeLedgerCursor(%q) = %v, want ErrInvalidCommand", c, err)
		}
	}
}
