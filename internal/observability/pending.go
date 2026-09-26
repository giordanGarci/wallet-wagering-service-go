package observability

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/junglegaming/backend-challenge-go/internal/app"
)

// PendingMetrics implements app.PendingMetrics:
//
//   - wallet_pending_reference_retries_total: retries that still found no
//     usable Reference and scheduled another one;
//   - wallet_pending_reference_resolutions_total{outcome}: processed,
//     rejected, expired (REFERENCE_NOT_FOUND) or failed (PERMANENT_ERROR);
//   - wallet_pending_reference_backlog: PENDING_REFERENCE rows, updated by
//     the worker every cycle.
type PendingMetrics struct {
	retries     prometheus.Counter
	resolutions *prometheus.CounterVec
	backlog     prometheus.Gauge
}

var _ app.PendingMetrics = (*PendingMetrics)(nil)

// NewPendingMetrics registers the collectors on reg.
func NewPendingMetrics(reg prometheus.Registerer) (*PendingMetrics, error) {
	m := &PendingMetrics{
		retries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_pending_reference_retries_total",
			Help: "Pending Reference retries that still found no usable Reference and scheduled another one.",
		}),
		resolutions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_pending_reference_resolutions_total",
			Help: "Pending References concluded by the worker, by outcome (processed, rejected, expired, failed).",
		}, []string{"outcome"}),
		backlog: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "wallet_pending_reference_backlog",
			Help: "Wager Transactions in PENDING_REFERENCE, updated every worker cycle.",
		}),
	}
	for _, c := range []prometheus.Collector{m.retries, m.resolutions, m.backlog} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Retry counts a retry that scheduled another one.
func (m *PendingMetrics) Retry() { m.retries.Inc() }

// Resolved counts a concluded Pending Reference.
func (m *PendingMetrics) Resolved(outcome app.PendingOutcome) {
	m.resolutions.WithLabelValues(string(outcome)).Inc()
}

// SetBacklog records the number of PENDING_REFERENCE rows.
func (m *PendingMetrics) SetBacklog(n int64) { m.backlog.Set(float64(n)) }
