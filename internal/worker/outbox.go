package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/crashpoint"
	"github.com/junglegaming/backend-challenge-go/internal/postgres"
	"github.com/junglegaming/backend-challenge-go/internal/sqs"
)

// OutboxModule runs the outbox publisher (role outbox): it provides the
// Publisher (the SQS events queue) and starts the publishing Loop with the
// app. The Loop depends on the pool and the SQS client, so Fx stops it before
// closing them.
var OutboxModule = fx.Module("outbox",
	fx.Provide(
		postgres.NewOutboxStore,
		fx.Annotate(sqs.NewEventPublisher, fx.As(new(Publisher))),
		NewOutboxPublisher,
	),
	fx.Invoke(func(lc fx.Lifecycle, p *OutboxPublisher) { lc.Append(p.Loop.Hook()) }),
)

// Publisher sends one Outbox Event. *sqs.EventPublisher implements it; tests
// decorate it to count or block sends.
type Publisher interface {
	Publish(ctx context.Context, groupID, deduplicationID, body string) error
}

// maxSendTimeout bounds a single SQS send (the SDK retries inside it).
const maxSendTimeout = 10 * time.Second

// OutboxPublisher publishes claimed Outbox Events in seq order.
type OutboxPublisher struct {
	Loop *Loop

	store     *postgres.OutboxStore
	publisher Publisher
	cfg       config.Outbox
	backoff   Backoff
	metrics   outboxMetrics
	log       *slog.Logger
}

// OutboxParams are the dependencies of NewOutboxPublisher.
type OutboxParams struct {
	fx.In
	Store     *postgres.OutboxStore
	Publisher Publisher
	Config    config.Config
	Registry  prometheus.Registerer
	Log       *slog.Logger
}

// NewOutboxPublisher builds the publisher and its Loop (not started).
func NewOutboxPublisher(p OutboxParams) (*OutboxPublisher, error) {
	m, err := newOutboxMetrics(p.Registry)
	if err != nil {
		return nil, err
	}
	o := &OutboxPublisher{
		store:     p.Store,
		publisher: p.Publisher,
		cfg:       p.Config.Outbox,
		backoff:   Backoff{Base: p.Config.Outbox.RetryBase, Max: p.Config.Outbox.RetryMax},
		metrics:   m,
		log:       p.Log.With("worker", "outbox"),
	}
	o.Loop = NewLoop("outbox", o.cfg.PollInterval, o.cycle, p.Log)
	return o, nil
}

// cycle claims one batch and publishes it in seq order:
//
//   - each event: crashpoint outbox.before_publish, SendMessage,
//     crashpoint outbox.after_publish, then published_at (lease-guarded);
//   - a failed send schedules the event with Backoff and skips the rest of
//     its aggregate in this batch, so a Wallet's events never overtake each
//     other in the FIFO group;
//   - on shutdown (soft canceled) or when most of the lease has elapsed, the
//     events not yet attempted are released for any publisher to take.
func (o *OutboxPublisher) cycle(soft, hard context.Context) bool {
	claim, err := o.store.Claim(soft, o.cfg.BatchSize, o.cfg.Lease)
	if err != nil {
		if soft.Err() == nil {
			o.log.WarnContext(soft, "outbox claim failed", "error", err)
		}
		return false
	}
	if len(claim.Records) == 0 {
		o.updateLag(soft)
		return false
	}

	// Stop starting sends once 4/5 of the lease has elapsed, leaving the
	// rest of it for the send in flight and its bookkeeping.
	sendBy := time.Now().Add(o.cfg.Lease - o.cfg.Lease/5)
	sendTimeout := min(o.cfg.Lease/2, maxSendTimeout)
	failed := map[uuid.UUID]bool{}
	var release []uuid.UUID

	for i, r := range claim.Records {
		if soft.Err() != nil || time.Now().After(sendBy) {
			for _, rest := range claim.Records[i:] {
				release = append(release, rest.EventID)
			}
			break
		}
		if failed[r.AggregateID] {
			release = append(release, r.EventID)
			continue
		}
		if !o.publish(hard, claim.Token, r, sendTimeout) {
			failed[r.AggregateID] = true
		}
	}

	if len(release) > 0 {
		n, err := o.store.Release(hard, release, claim.Token)
		if err != nil {
			o.log.WarnContext(hard, "outbox lease release failed; leases expire on their own", "error", err, "events", len(release))
		} else {
			o.log.InfoContext(hard, "outbox leases released", "released", n)
		}
	}
	o.updateLag(soft)
	return true
}

// publish sends one event and records the outcome. It reports whether the
// event reached SQS.
func (o *OutboxPublisher) publish(ctx context.Context, token time.Time, r postgres.OutboxRecord, timeout time.Duration) bool {
	ctx = app.WithCorrelationID(ctx, r.CorrelationID)
	log := o.log.With("eventId", r.EventID, "walletId", r.AggregateID, "attempts", r.Attempts)
	if r.Attempts > 0 {
		o.metrics.retries.Inc()
	}

	crashpoint.Hit(crashpoint.OutboxBeforePublish)
	sendCtx, cancel := context.WithTimeout(ctx, timeout)
	err := o.publisher.Publish(sendCtx, r.AggregateID.String(), r.EventID.String(), r.Payload)
	cancel()
	if err != nil {
		o.metrics.failures.Inc()
		retryIn := o.backoff.Delay(r.Attempts + 1)
		log.WarnContext(ctx, "outbox publish failed, retry scheduled", "error", err, "retry_in", retryIn)
		if _, markErr := o.store.MarkFailed(ctx, r.EventID, token, retryIn, err.Error()); markErr != nil {
			log.WarnContext(ctx, "outbox failure not recorded; lease expires on its own", "error", markErr)
		}
		return false
	}
	crashpoint.Hit(crashpoint.OutboxAfterPublish)

	o.metrics.published.Inc()
	owned, err := o.store.MarkPublished(ctx, r.EventID, token)
	switch {
	case err != nil:
		log.WarnContext(ctx, "outbox event sent but not recorded; it will be republished with the same eventId", "error", err)
	case !owned:
		log.WarnContext(ctx, "outbox lease lost before recording publication; the event may be republished with the same eventId")
	default:
		log.DebugContext(ctx, "outbox event published")
	}
	return true
}

func (o *OutboxPublisher) updateLag(ctx context.Context) {
	age, err := o.store.OldestUnpublishedAge(ctx)
	if err != nil {
		return
	}
	o.metrics.lag.Set(age.Seconds())
}

type outboxMetrics struct {
	lag       prometheus.Gauge
	published prometheus.Counter
	failures  prometheus.Counter
	retries   prometheus.Counter
}

func newOutboxMetrics(reg prometheus.Registerer) (outboxMetrics, error) {
	m := outboxMetrics{
		lag: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "wallet_outbox_lag_seconds",
			Help: "Age of the oldest unpublished Outbox Event (0 when none), updated every publisher cycle.",
		}),
		published: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_outbox_published_total",
			Help: "Outbox Events sent to the events queue.",
		}),
		failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_outbox_publish_failures_total",
			Help: "Failed Outbox Event sends; each one schedules a retry with backoff.",
		}),
		retries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_outbox_retries_total",
			Help: "Send attempts of Outbox Events that had failed before.",
		}),
	}
	for _, c := range []prometheus.Collector{m.lag, m.published, m.failures, m.retries} {
		if err := reg.Register(c); err != nil {
			return outboxMetrics{}, err
		}
	}
	return m, nil
}
