package observability

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/junglegaming/backend-challenge-go/internal/app"
)

// ReconciliationMetrics implements app.ReconciliationMetrics:
//
//   - wallet_reconciliations_total{result="consistent"|"divergent"}
//   - wallet_reconciliation_divergences_total, the series to alert on.
type ReconciliationMetrics struct {
	results     *prometheus.CounterVec
	divergences prometheus.Counter
}

var _ app.ReconciliationMetrics = (*ReconciliationMetrics)(nil)

// NewReconciliationMetrics registers the counters on reg. Both result
// labels are exposed from the start at zero.
func NewReconciliationMetrics(reg prometheus.Registerer) (*ReconciliationMetrics, error) {
	m := &ReconciliationMetrics{
		results: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_reconciliations_total",
			Help: "Wallet Reconciliations run, by result (consistent or divergent).",
		}, []string{"result"}),
		divergences: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_reconciliation_divergences_total",
			Help: "Reconciliations whose stored balance differs from the balance rebuilt from the ledger.",
		}),
	}
	for _, c := range []prometheus.Collector{m.results, m.divergences} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	m.results.WithLabelValues("consistent")
	m.results.WithLabelValues("divergent")
	return m, nil
}

// Observe counts one Reconciliation.
func (m *ReconciliationMetrics) Observe(consistent bool) {
	if consistent {
		m.results.WithLabelValues("consistent").Inc()
		return
	}
	m.results.WithLabelValues("divergent").Inc()
	m.divergences.Inc()
}
