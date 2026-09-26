package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// IdempotencyKeyHeader carries the Idempotency Key of POST
// /wagering/transactions. It is required and used exactly as sent.
const IdempotencyKeyHeader = "Idempotency-Key"

// Error codes of the wagering endpoints (409).
const (
	CodeIdempotencyKeyConflict      = "IDEMPOTENCY_KEY_CONFLICT"
	CodeExternalTransactionConflict = "EXTERNAL_TRANSACTION_CONFLICT"
)

// WagerHandlers serves the Provider endpoints.
type WagerHandlers struct {
	process *app.ProcessWagerTransaction
	get     *app.GetWagerTransaction
	log     *slog.Logger
}

// NewWagerHandlers builds the handlers.
func NewWagerHandlers(process *app.ProcessWagerTransaction, get *app.GetWagerTransaction, log *slog.Logger) *WagerHandlers {
	return &WagerHandlers{process: process, get: get, log: log}
}

// PostWagerRoute is POST /wagering/transactions, Provider clients only.
func PostWagerRoute(h *WagerHandlers, auth *Authenticator) Route {
	return Route{Pattern: "POST /wagering/transactions", Handler: auth.Provider(http.HandlerFunc(h.Post))}
}

// GetWagerRoute is GET /wagering/transactions/{transactionId}.
func GetWagerRoute(h *WagerHandlers, auth *Authenticator) Route {
	return Route{Pattern: "GET /wagering/transactions/{transactionId}", Handler: auth.Provider(http.HandlerFunc(h.GetByID))}
}

// GetWagerByExternalIDRoute is
// GET /providers/{providerId}/wagering/transactions/{externalTransactionId}.
func GetWagerByExternalIDRoute(h *WagerHandlers, auth *Authenticator) Route {
	return Route{
		Pattern: "GET /providers/{providerId}/wagering/transactions/{externalTransactionId}",
		Handler: auth.Provider(http.HandlerFunc(h.GetByExternalID)),
	}
}

// wagerRequest is the strict body of POST /wagering/transactions.
type wagerRequest struct {
	ProviderID                     string       `json:"providerId"`
	ExternalTransactionID          string       `json:"externalTransactionId"`
	PlayerID                       string       `json:"playerId"`
	WalletID                       string       `json:"walletId"`
	RoundID                        string       `json:"roundId"`
	GameID                         string       `json:"gameId"`
	Kind                           string       `json:"kind"`
	Money                          *money.Money `json:"money"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId"`
}

// wagerResponse is the result of POST /wagering/transactions.
type wagerResponse struct {
	TransactionID    uuid.UUID    `json:"transactionId"`
	Status           wager.Status `json:"status"`
	Balance          *money.Money `json:"balance"`
	FailureCode      string       `json:"failureCode,omitempty"`
	IdempotentReplay bool         `json:"idempotentReplay"`
}

// wagerView is the body of the GET endpoints.
type wagerView struct {
	TransactionID                  uuid.UUID    `json:"transactionId"`
	ProviderID                     string       `json:"providerId"`
	ExternalTransactionID          string       `json:"externalTransactionId"`
	Kind                           wager.Kind   `json:"kind"`
	Status                         wager.Status `json:"status"`
	WalletID                       uuid.UUID    `json:"walletId"`
	PlayerID                       uuid.UUID    `json:"playerId"`
	RoundID                        string       `json:"roundId"`
	GameID                         string       `json:"gameId"`
	Money                          money.Money  `json:"money"`
	Balance                        *money.Money `json:"balance"`
	FailureCode                    string       `json:"failureCode,omitempty"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId,omitempty"`
	CreatedAt                      string       `json:"createdAt"`
	UpdatedAt                      string       `json:"updatedAt"`
}

func balanceOf(t *wager.Transaction) *money.Money {
	if b, ok := t.BalanceAfter(); ok {
		return &b
	}
	return nil
}

func toWagerView(t *wager.Transaction) wagerView {
	return wagerView{
		TransactionID:                  t.ID(),
		ProviderID:                     t.ProviderID(),
		ExternalTransactionID:          t.ExternalTransactionID(),
		Kind:                           t.Kind(),
		Status:                         t.Status(),
		WalletID:                       t.WalletID(),
		PlayerID:                       t.PlayerID(),
		RoundID:                        t.RoundID(),
		GameID:                         t.GameID(),
		Money:                          t.Money(),
		Balance:                        balanceOf(t),
		FailureCode:                    string(t.FailureCode()),
		ReferenceExternalTransactionID: t.ReferenceExternalTransactionID(),
		CreatedAt:                      t.CreatedAt().UTC().Format(time.RFC3339Nano),
		UpdatedAt:                      t.UpdatedAt().UTC().Format(time.RFC3339Nano),
	}
}

// wagerStatusCode maps a result status to HTTP: 200 PROCESSED, 422
// REJECTED, 202 PENDING_REFERENCE (and PENDING), 500 FAILED.
func wagerStatusCode(s wager.Status) int {
	switch s {
	case wager.StatusProcessed:
		return http.StatusOK
	case wager.StatusRejected:
		return http.StatusUnprocessableEntity
	case wager.StatusFailed:
		return http.StatusInternalServerError
	default:
		return http.StatusAccepted
	}
}

// Post handles POST /wagering/transactions. Checks, in order: token (401 /
// 403 non-Provider), Idempotency-Key present (400), strict body (400),
// body providerId equal to the token's provider_id (403), format rules
// (400); only then the use case runs.
func (h *WagerHandlers) Post(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	key := r.Header.Get(IdempotencyKeyHeader)
	if key == "" {
		WriteInvalidRequest(w, errors.New("Idempotency-Key header is required"))
		return
	}
	var req wagerRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteInvalidRequest(w, err)
		return
	}
	if req.ProviderID != "" && req.ProviderID != p.ProviderID {
		WriteError(w, http.StatusForbidden, CodeForbidden, "providerId does not match the authenticated provider")
		return
	}
	in := wager.CommandInput{
		ProviderID:                     req.ProviderID,
		ExternalTransactionID:          req.ExternalTransactionID,
		IdempotencyKey:                 key,
		PlayerID:                       req.PlayerID,
		WalletID:                       req.WalletID,
		RoundID:                        req.RoundID,
		GameID:                         req.GameID,
		Kind:                           req.Kind,
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
	}
	if req.Money != nil {
		in.Money = *req.Money
	}
	cmd, err := wager.NewCommand(in)
	if err != nil {
		WriteInvalidRequest(w, errors.New(detail(err, wager.ErrInvalidCommand)))
		return
	}

	res, err := h.process.Execute(r.Context(), app.WagerRequest{Command: cmd, Channel: app.ChannelHTTP})
	switch {
	case errors.Is(err, wager.ErrIdempotencyKeyConflict):
		WriteError(w, http.StatusConflict, CodeIdempotencyKeyConflict, "Idempotency-Key was already used with a different payload")
		return
	case errors.Is(err, wager.ErrExternalTransactionConflict):
		WriteError(w, http.StatusConflict, CodeExternalTransactionConflict, "externalTransactionId was already recorded under another Idempotency-Key")
		return
	case err != nil:
		WriteAppError(w, r, h.log, err)
		return
	}
	t := res.Transaction
	WriteJSON(w, wagerStatusCode(t.Status()), wagerResponse{
		TransactionID:    t.ID(),
		Status:           t.Status(),
		Balance:          balanceOf(t),
		FailureCode:      string(t.FailureCode()),
		IdempotentReplay: res.Replay,
	})
}

// GetByID handles GET /wagering/transactions/{transactionId}.
func (h *WagerHandlers) GetByID(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	id, err := uuid.Parse(r.PathValue("transactionId"))
	if err != nil {
		WriteInvalidRequest(w, errors.New("transactionId must be a UUID"))
		return
	}
	t, err := h.get.ByID(r.Context(), p.ProviderID, id)
	h.writeView(w, r, t, err)
}

// GetByExternalID handles
// GET /providers/{providerId}/wagering/transactions/{externalTransactionId}.
func (h *WagerHandlers) GetByExternalID(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	if r.PathValue("providerId") != p.ProviderID {
		WriteError(w, http.StatusForbidden, CodeForbidden, "providerId does not match the authenticated provider")
		return
	}
	t, err := h.get.ByExternalID(r.Context(), p.ProviderID, r.PathValue("externalTransactionId"))
	h.writeView(w, r, t, err)
}

func (h *WagerHandlers) writeView(w http.ResponseWriter, r *http.Request, t *wager.Transaction, err error) {
	if err != nil {
		WriteAppError(w, r, h.log, err)
		return
	}
	WriteJSON(w, http.StatusOK, toWagerView(t))
}
