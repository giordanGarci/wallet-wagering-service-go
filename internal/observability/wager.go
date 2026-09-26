package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// WagerMetrics implements app.WagerMetrics:
//
//   - wallet_wager_transactions_total{kind,status}: outcomes of new
//     Wager Transactions (replays excluded);
//   - wallet_wager_duplicates_total{outcome}: replays and idempotency
//     conflicts;
//   - wallet_concurrency_conflicts_total: versioned Wallet updates that
//     affected no row;
//   - wallet_wager_processing_seconds{channel,result}: use case latency.
type WagerMetrics struct {
	outcomes   *prometheus.CounterVec
	duplicates *prometheus.CounterVec
	conflicts  prometheus.Counter
	latency    *prometheus.HistogramVec
}

var _ app.WagerMetrics = (*WagerMetrics)(nil)

// NewWagerMetrics registers the collectors on reg.
func NewWagerMetrics(reg prometheus.Registerer) (*WagerMetrics, error) {
	m := &WagerMetrics{
		outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_wager_transactions_total",
			Help: "Wager Transactions recorded, by kind and resulting status (replays excluded).",
		}, []string{"kind", "status"}),
		duplicates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_wager_duplicates_total",
			Help: "Repeated operations: idempotent replays and idempotency conflicts.",
		}, []string{"outcome"}),
		conflicts: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_concurrency_conflicts_total",
			Help: "Versioned wallet updates that affected no row.",
		}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "wallet_wager_processing_seconds",
			Help:    "Latency of ProcessWagerTransaction, by channel and result.",
			Buckets: prometheus.DefBuckets,
		}, []string{"channel", "result"}),
	}
	for _, c := range []prometheus.Collector{m.outcomes, m.duplicates, m.conflicts, m.latency} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Recorded counts a new Wager Transaction.
func (m *WagerMetrics) Recorded(kind wager.Kind, status wager.Status) {
	m.outcomes.WithLabelValues(string(kind), string(status)).Inc()
}

// Duplicate counts a replay or an idempotency conflict.
func (m *WagerMetrics) Duplicate(outcome string) { m.duplicates.WithLabelValues(outcome).Inc() }

// ConcurrencyConflict counts a versioned Wallet update that affected no row.
func (m *WagerMetrics) ConcurrencyConflict() { m.conflicts.Inc() }

// Observe records the use case latency.
func (m *WagerMetrics) Observe(channel app.Channel, result string, d time.Duration) {
	m.latency.WithLabelValues(string(channel), result).Observe(d.Seconds())
}
