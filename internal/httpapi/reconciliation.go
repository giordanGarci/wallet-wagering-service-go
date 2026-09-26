package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// ReconciliationHandler serves POST /wallets/{walletId}/reconciliation.
type ReconciliationHandler struct {
	reconcile *app.Reconcile
	log       *slog.Logger
}

// NewReconciliationHandler builds the handler.
func NewReconciliationHandler(reconcile *app.Reconcile, log *slog.Logger) *ReconciliationHandler {
	return &ReconciliationHandler{reconcile: reconcile, log: log}
}

// ReconciliationRoute is POST /wallets/{walletId}/reconciliation, internal
// client only.
func ReconciliationRoute(h *ReconciliationHandler, auth *Authenticator) Route {
	return Route{Pattern: "POST /wallets/{walletId}/reconciliation", Handler: auth.Internal(h)}
}

type reconciliationResponse struct {
	WalletID          uuid.UUID   `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int64       `json:"checkedEntries"`
}

// ServeHTTP answers 200 with the report whether or not the Wallet is
// consistent; a divergence is data, not an error.
func (h *ReconciliationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("walletId"))
	if err != nil {
		WriteInvalidRequest(w, errors.New("walletId must be a UUID"))
		return
	}
	rep, err := h.reconcile.Execute(r.Context(), id)
	if err != nil {
		WriteAppError(w, r, h.log, err)
		return
	}
	WriteJSON(w, http.StatusOK, reconciliationResponse{
		WalletID:          rep.WalletID,
		StoredBalance:     rep.StoredBalance,
		CalculatedBalance: rep.CalculatedBalance,
		Difference:        rep.Difference,
		Consistent:        rep.Consistent,
		CheckedEntries:    rep.CheckedEntries,
	})
}
