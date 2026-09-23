package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/junglegaming/backend-challenge-go/internal/app"
)

// Stable error codes of the error body. Clients branch on these, never on
// the message.
const (
	CodeInvalidRequest      = "INVALID_REQUEST"
	CodeUnauthenticated     = "UNAUTHENTICATED"
	CodeForbidden           = "FORBIDDEN"
	CodeNotFound            = "NOT_FOUND"
	CodeWalletAlreadyExists = "WALLET_ALREADY_EXISTS"
	CodeNotImplemented      = "NOT_IMPLEMENTED"
	CodeServiceUnavailable  = "SERVICE_UNAVAILABLE"
	CodeInternal            = "INTERNAL_ERROR"
)

// RetryAfterSeconds is sent with 503 responses.
const RetryAfterSeconds = "1"

// maxBodyBytes bounds request bodies read by DecodeJSON.
const maxBodyBytes = 1 << 20

// ErrorBody is the single error shape of the API.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON writes body as JSON with status.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// WriteError writes {"code": code, "message": message} with status.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", RetryAfterSeconds)
	}
	WriteJSON(w, status, ErrorBody{Code: code, Message: message})
}

// WriteAppError maps an error returned by a use case to its response. Known
// app errors carry their message to the client; anything else is logged and
// answered with a generic 500 so internals never leak.
func WriteAppError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	switch {
	case errors.Is(err, app.ErrInvalidCommand):
		WriteError(w, http.StatusBadRequest, CodeInvalidRequest, detail(err, app.ErrInvalidCommand))
	case errors.Is(err, app.ErrWalletNotFound):
		WriteError(w, http.StatusNotFound, CodeNotFound, "wallet not found")
	case errors.Is(err, app.ErrWalletAlreadyExists):
		WriteError(w, http.StatusConflict, CodeWalletAlreadyExists, "a wallet already exists for this player and currency")
	case errors.Is(err, app.ErrNotImplemented):
		WriteError(w, http.StatusNotImplemented, CodeNotImplemented, detail(err, app.ErrNotImplemented))
	case errors.Is(err, app.ErrTransient):
		log.WarnContext(r.Context(), "transient failure", "method", r.Method, "path", r.URL.Path, "error", err)
		WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "temporarily unavailable, retry later")
	default:
		log.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		WriteError(w, http.StatusInternalServerError, CodeInternal, "internal error")
	}
}

// detail returns the message of err without the "sentinel: " prefix the
// app layer adds, e.g. "playerId is required".
func detail(err, sentinel error) string {
	return strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
}

// DecodeJSON strictly decodes the request body into dst: one JSON value, no
// unknown fields, no trailing data, at most 1 MiB. The returned error is
// safe to show to the client (see WriteInvalidRequest).
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			return errors.New("request body is empty")
		case errors.As(err, &maxErr):
			return fmt.Errorf("request body exceeds %d bytes", maxErr.Limit)
		default:
			return fmt.Errorf("malformed JSON body: %w", err)
		}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

// WriteInvalidRequest answers 400 INVALID_REQUEST with err's message.
func WriteInvalidRequest(w http.ResponseWriter, err error) {
	WriteError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error())
}
