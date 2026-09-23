package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// WalletHandlers serves the internal Wallet endpoints.
type WalletHandlers struct {
	open *app.OpenWallet
	get  *app.GetWallet
	log  *slog.Logger
}

// NewWalletHandlers builds the handlers.
func NewWalletHandlers(open *app.OpenWallet, get *app.GetWallet, log *slog.Logger) *WalletHandlers {
	return &WalletHandlers{open: open, get: get, log: log}
}

// OpenWalletRoute is POST /wallets, internal client only.
func OpenWalletRoute(h *WalletHandlers, auth *Authenticator) Route {
	return Route{Pattern: "POST /wallets", Handler: auth.Internal(http.HandlerFunc(h.Open))}
}

// GetWalletRoute is GET /wallets/{walletId}, internal client only.
func GetWalletRoute(h *WalletHandlers, auth *Authenticator) Route {
	return Route{Pattern: "GET /wallets/{walletId}", Handler: auth.Internal(http.HandlerFunc(h.Get))}
}

type openWalletRequest struct {
	PlayerID       uuid.UUID    `json:"playerId"`
	InitialBalance *money.Money `json:"initialBalance"`
}

type walletResponse struct {
	ID        uuid.UUID   `json:"id"`
	PlayerID  uuid.UUID   `json:"playerId"`
	Balance   money.Money `json:"balance"`
	Version   int64       `json:"version"`
	CreatedAt string      `json:"createdAt"`
}

func toWalletResponse(w *wallet.Wallet) walletResponse {
	return walletResponse{
		ID:        w.ID(),
		PlayerID:  w.PlayerID(),
		Balance:   w.Balance(),
		Version:   w.Version(),
		CreatedAt: w.CreatedAt().UTC().Format(time.RFC3339Nano),
	}
}

// Open handles POST /wallets.
func (h *WalletHandlers) Open(w http.ResponseWriter, r *http.Request) {
	var req openWalletRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteInvalidRequest(w, err)
		return
	}
	if req.PlayerID == uuid.Nil {
		WriteInvalidRequest(w, errors.New("playerId is required"))
		return
	}
	if req.InitialBalance == nil {
		WriteInvalidRequest(w, errors.New("initialBalance is required"))
		return
	}

	opened, err := h.open.Execute(r.Context(), app.OpenWalletCommand{PlayerID: req.PlayerID, InitialBalance: *req.InitialBalance})
	if err != nil {
		WriteAppError(w, r, h.log, err)
		return
	}
	w.Header().Set("Location", "/wallets/"+opened.ID().String())
	WriteJSON(w, http.StatusCreated, toWalletResponse(opened))
}

// Get handles GET /wallets/{walletId}.
func (h *WalletHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("walletId"))
	if err != nil {
		WriteInvalidRequest(w, errors.New("walletId must be a UUID"))
		return
	}
	found, err := h.get.Execute(r.Context(), id)
	if err != nil {
		WriteAppError(w, r, h.log, err)
		return
	}
	WriteJSON(w, http.StatusOK, toWalletResponse(found))
}
