package httpapi

import (
	"net/http"

	"github.com/junglegaming/backend-challenge-go/internal/app"
)

// CorrelationHeader carries the correlation id of a request and is echoed
// on every response.
const CorrelationHeader = "X-Correlation-Id"

// maxCorrelationIDLen bounds accepted client ids.
const maxCorrelationIDLen = 128

// WithCorrelation stores the request's correlation id in the context
// (app.WithCorrelationID) and echoes it in the X-Correlation-Id response
// header. A missing or invalid header (longer than 128 characters or with
// characters outside [A-Za-z0-9._:-]) is replaced by a fresh UUIDv7, so
// logs and Outbox Events always carry a safe id.
func WithCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationHeader)
		if !validCorrelationID(id) {
			id = app.NewCorrelationID()
		}
		w.Header().Set(CorrelationHeader, id)
		next.ServeHTTP(w, r.WithContext(app.WithCorrelationID(r.Context(), id)))
	})
}

func validCorrelationID(id string) bool {
	if id == "" || len(id) > maxCorrelationIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '.', c == '_', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}
