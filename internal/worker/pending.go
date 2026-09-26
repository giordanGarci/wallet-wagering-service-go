package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/crashpoint"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"github.com/junglegaming/backend-challenge-go/internal/postgres"
)

// PendingModule runs the Pending Reference worker (role pending): it
// provides the store, the ResumePendingReference use case and its metrics,
// and starts the Loop with the app. The Loop depends on the pool, so Fx
// stops it before closing the pool.
var PendingModule = fx.Module("pending",
	fx.Provide(
		postgres.NewPendingStore,
		fx.Annotate(observability.NewPendingMetrics, fx.As(new(app.PendingMetrics))),
		app.NewResumePendingReference,
		NewPendingWorker,
	),
	fx.Invoke(func(lc fx.Lifecycle, w *PendingWorker) { lc.Append(w.Loop.Hook()) }),
)

// PendingWorker retries due Pending References.
type PendingWorker struct {
	Loop *Loop

	store   *postgres.PendingStore
	resume  *app.ResumePendingReference
	metrics app.PendingMetrics
	cfg     config.Pending
	log     *slog.Logger
}

// PendingParams are the dependencies of NewPendingWorker.
type PendingParams struct {
	fx.In
	Store   *postgres.PendingStore
	Resume  *app.ResumePendingReference
	Metrics app.PendingMetrics
	Config  config.Config
	Log     *slog.Logger
}

// NewPendingWorker builds the worker and its Loop (not started).
func NewPendingWorker(p PendingParams) *PendingWorker {
	w := &PendingWorker{
		store:   p.Store,
		resume:  p.Resume,
		metrics: p.Metrics,
		cfg:     p.Config.Pending,
		log:     p.Log.With("worker", "pending"),
	}
	w.Loop = NewLoop("pending", w.cfg.PollInterval, w.cycle, p.Log)
	return w
}

// cycle claims one batch of due Pending References and retries each one in
// its own SQL transaction (app.ResumePendingReference):
//
//   - crashpoint pending.after_claim right after the claim commits: a crash
//     there leaves leased rows that another instance takes over once the
//     lease expires;
//   - a retry that fails (transient error) keeps its lease, so the row is
//     retried after the lease expires, by any instance;
//   - on shutdown (soft canceled) or when most of the lease has elapsed, the
//     retry in progress finishes under hard and the rows not yet attempted
//     are released for any worker to take.
func (w *PendingWorker) cycle(soft, hard context.Context) bool {
	claim, err := w.store.Claim(soft, w.cfg.BatchSize, w.cfg.Lease)
	if err != nil {
		if soft.Err() == nil {
			w.log.WarnContext(soft, "pending reference claim failed", "error", err)
		}
		return false
	}
	if len(claim.Items) == 0 {
		w.updateBacklog(soft)
		return false
	}
	crashpoint.Hit(crashpoint.PendingAfterClaim)

	// Stop starting retries once 4/5 of the lease has elapsed, leaving the
	// rest of it for the retry in flight.
	startBy := time.Now().Add(w.cfg.Lease - w.cfg.Lease/5)
	var release []uuid.UUID
	for i, item := range claim.Items {
		if soft.Err() != nil || time.Now().After(startBy) {
			for _, rest := range claim.Items[i:] {
				release = append(release, rest.TransactionID)
			}
			break
		}
		// Retries run under hard: a graceful stop lets the one in progress
		// commit; past the deadline it rolls back and the lease expires.
		_, _ = w.resume.Execute(hard, item)
	}
	if len(release) > 0 {
		n, err := w.store.Release(hard, release, claim.Token)
		if err != nil {
			w.log.WarnContext(hard, "pending reference lease release failed; leases expire on their own",
				"error", err, "rows", len(release))
		} else {
			w.log.InfoContext(hard, "pending reference leases released", "released", n)
		}
	}
	w.updateBacklog(soft)
	return true
}

func (w *PendingWorker) updateBacklog(ctx context.Context) {
	n, err := w.store.Backlog(ctx)
	if err != nil {
		return
	}
	w.metrics.SetBacklog(n)
}
