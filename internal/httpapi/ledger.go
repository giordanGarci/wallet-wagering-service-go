package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// LedgerRoute is GET /wallets/{walletId}/ledger?cursor=&limit=, internal
// client only.
func LedgerRoute(h *WalletHandlers, auth *Authenticator) Route {
	return Route{Pattern: "GET /wallets/{walletId}/ledger", Handler: auth.Internal(http.HandlerFunc(h.Ledger))}
}

type ledgerEntryResponse struct {
	ID            uuid.UUID        `json:"id"`
	TransactionID uuid.UUID        `json:"transactionId"`
	Direction     ledger.Direction `json:"direction"`
	Money         money.Money      `json:"money"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	CreatedAt     string           `json:"createdAt"`
}

type ledgerPageResponse struct {
	Items      []ledgerEntryResponse `json:"items"`
	NextCursor *string               `json:"nextCursor"`
}

// Ledger handles GET /wallets/{walletId}/ledger: entries in stable
// (createdAt, id) order, limit 1..200 (default 50), opaque cursor from the
// previous page's nextCursor (null on the last page).
func (h *WalletHandlers) Ledger(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("walletId"))
	if err != nil {
		WriteInvalidRequest(w, errors.New("walletId must be a UUID"))
		return
	}
	q := r.URL.Query()
	query := app.ListLedgerQuery{WalletID: id, Cursor: q.Get("cursor")}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			WriteInvalidRequest(w, errors.New("limit must be an integer between 1 and "+strconv.Itoa(app.MaxLedgerLimit)))
			return
		}
		query.Limit = n
	}

	page, err := h.ledger.Execute(r.Context(), query)
	if err != nil {
		WriteAppError(w, r, h.log, err)
		return
	}
	resp := ledgerPageResponse{Items: make([]ledgerEntryResponse, 0, len(page.Entries))}
	for _, e := range page.Entries {
		resp.Items = append(resp.Items, ledgerEntryResponse{
			ID:            e.ID(),
			TransactionID: e.TransactionID(),
			Direction:     e.Direction(),
			Money:         e.Money(),
			BalanceBefore: e.BalanceBefore(),
			BalanceAfter:  e.BalanceAfter(),
			CreatedAt:     e.CreatedAt().UTC().Format(time.RFC3339Nano),
		})
	}
	if page.NextCursor != "" {
		resp.NextCursor = &page.NextCursor
	}
	WriteJSON(w, http.StatusOK, resp)
}
