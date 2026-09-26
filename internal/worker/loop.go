// Package worker runs the background workers: the outbox publisher (role
// outbox) and, in ticket 11, the Pending Reference worker (role pending).
//
// Both follow the same pattern: a Loop owned by fx.Lifecycle repeatedly runs
// a cycle that claims rows under a lease (FOR UPDATE SKIP LOCKED), works on
// them and records each outcome guarded by the lease, retrying failures with
// Backoff.
package worker

import (
	"context"
	"log/slog"
	"time"

	"go.uber.org/fx"
)

// Cycle is one unit of work of a Loop.
//
// soft is canceled when the app starts stopping: the cycle must not claim or
// start new work after that, and releases what it claimed but did not start.
// hard is canceled when the stop deadline (SHUTDOWN_TIMEOUT) expires: work
// already started (an SQS send, a SQL write) runs under it, so it can finish
// during a graceful stop and is interrupted only past the deadline.
//
// It returns true when it found work, so the Loop runs again at once instead
// of waiting for the poll interval.
type Cycle func(soft, hard context.Context) (busy bool)

// Loop runs a Cycle until stopped. Start and Stop are its fx.Lifecycle hooks.
type Loop struct {
	name     string
	interval time.Duration
	cycle    Cycle
	log      *slog.Logger

	cancelSoft context.CancelFunc
	cancelHard context.CancelFunc
	soft, hard context.Context
	done       chan struct{}
}

// NewLoop builds a Loop that runs cycle, pausing for interval whenever a
// cycle finds no work.
func NewLoop(name string, interval time.Duration, cycle Cycle, log *slog.Logger) *Loop {
	l := &Loop{name: name, interval: interval, cycle: cycle, log: log.With("worker", name), done: make(chan struct{})}
	l.hard, l.cancelHard = context.WithCancel(context.Background())
	l.soft, l.cancelSoft = context.WithCancel(l.hard)
	return l
}

// Hook returns the lifecycle hook that starts the loop and stops it.
func (l *Loop) Hook() fx.Hook {
	return fx.Hook{
		OnStart: func(ctx context.Context) error {
			l.Start()
			l.log.InfoContext(ctx, "worker started", "poll_interval", l.interval)
			return nil
		},
		OnStop: l.Stop,
	}
}

// Start spawns the loop goroutine.
func (l *Loop) Start() {
	go l.run()
}

// Stop asks the loop to stop and waits for its goroutine to exit. The
// running cycle finishes the work it started (and releases the rest) until
// ctx expires; then its in-flight work is canceled and Stop still waits for
// the goroutine, returning ctx's error.
func (l *Loop) Stop(ctx context.Context) error {
	l.cancelSoft()
	select {
	case <-l.done:
		l.cancelHard()
		l.log.InfoContext(ctx, "worker stopped")
		return nil
	case <-ctx.Done():
		l.cancelHard()
		<-l.done
		l.log.WarnContext(ctx, "worker stopped at the shutdown deadline; unfinished leases expire on their own")
		return ctx.Err()
	}
}

// Done is closed when the loop goroutine has exited.
func (l *Loop) Done() <-chan struct{} { return l.done }

func (l *Loop) run() {
	defer close(l.done)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-l.soft.Done():
			return
		case <-timer.C:
		}
		if l.cycle(l.soft, l.hard) {
			timer.Reset(0)
		} else {
			timer.Reset(l.interval)
		}
	}
}
