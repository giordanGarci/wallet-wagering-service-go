package postgres

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OutboxStore is the publisher side of outbox_events: claim due events under
// a lease, then record each outcome.
//
// Lease token: every row of one claim gets the same locked_until
// (now() + lease, computed once by the claiming statement), returned as
// Claim.Token. A row is only reclaimed after its lease expired, so a later
// claim always sets a strictly greater locked_until: comparing locked_until
// with the token tells a publisher whether it still owns the row, without a
// separate owner column. Every write after the claim is guarded by it, so a
// publisher that lost its lease (paused past it, taken over) changes nothing.
type OutboxStore struct {
	pool *pgxpool.Pool
}

// NewOutboxStore binds the store to pool.
func NewOutboxStore(pool *pgxpool.Pool) *OutboxStore {
	return &OutboxStore{pool: pool}
}

// OutboxRecord is a claimed, unpublished Outbox Event.
type OutboxRecord struct {
	EventID       uuid.UUID
	Seq           int64
	AggregateID   uuid.UUID
	CorrelationID string
	// Payload is the exact envelope stored in the commit that produced it.
	Payload string
	// Attempts counts earlier failed publications.
	Attempts int
}

// OutboxClaim is the result of one Claim: records in seq order and the lease
// token that guards them.
type OutboxClaim struct {
	Records []OutboxRecord
	Token   time.Time
}

// Claim leases up to limit due, unpublished events in seq order and commits
// at once (single statement, so row locks last only for the claim).
//
// Candidates: published_at IS NULL, next_attempt_at <= now() and no live
// lease, locked with FOR UPDATE SKIP LOCKED so concurrent publishers split
// the work. To keep a Wallet's events in order, a candidate is leased only
// when every older unpublished event of its aggregate is in the same claim;
// otherwise it waits (an older event is leased elsewhere, backing off, or
// beyond the limit).
func (s *OutboxStore) Claim(ctx context.Context, limit int, lease time.Duration) (OutboxClaim, error) {
	rows, err := s.pool.Query(ctx, `
		WITH candidates AS (
			SELECT event_id, aggregate_id, seq
			FROM outbox_events
			WHERE published_at IS NULL
			  AND next_attempt_at <= now()
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY seq
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE outbox_events o
		SET locked_until = now() + $2::float8 * interval '1 second'
		FROM candidates c
		WHERE o.event_id = c.event_id
		  AND NOT EXISTS (
			SELECT 1 FROM outbox_events p
			WHERE p.aggregate_id = c.aggregate_id
			  AND p.published_at IS NULL
			  AND p.seq < c.seq
			  AND p.event_id NOT IN (SELECT event_id FROM candidates))
		RETURNING o.event_id, o.seq, o.aggregate_id, o.correlation_id, o.payload, o.attempts, o.locked_until`,
		limit, lease.Seconds())
	if err != nil {
		return OutboxClaim{}, classify(fmt.Errorf("postgres: claim outbox events: %w", err))
	}
	defer rows.Close()
	var claim OutboxClaim
	for rows.Next() {
		var r OutboxRecord
		if err := rows.Scan(&r.EventID, &r.Seq, &r.AggregateID, &r.CorrelationID, &r.Payload, &r.Attempts, &claim.Token); err != nil {
			return OutboxClaim{}, fmt.Errorf("postgres: scan claimed outbox event: %w", err)
		}
		claim.Records = append(claim.Records, r)
	}
	if err := rows.Err(); err != nil {
		return OutboxClaim{}, classify(fmt.Errorf("postgres: claim outbox events: %w", err))
	}
	slices.SortFunc(claim.Records, func(a, b OutboxRecord) int { return cmp.Compare(a.Seq, b.Seq) })
	return claim, nil
}

// MarkPublished records the publication of eventID and drops its lease. It
// reports false when the lease identified by token is no longer held (the
// event was reclaimed or already recorded); the SQS message was still sent,
// and a republication keeps the same eventId.
func (s *OutboxStore) MarkPublished(ctx context.Context, eventID uuid.UUID, token time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE outbox_events
		SET published_at = now(), locked_until = NULL, last_error = NULL
		WHERE event_id = $1 AND locked_until = $2 AND published_at IS NULL`,
		eventID, token)
	if err != nil {
		return false, classify(fmt.Errorf("postgres: mark outbox event %s published: %w", eventID, err))
	}
	return tag.RowsAffected() == 1, nil
}

// MarkFailed records a failed publication: one more attempt, the next one
// not before now() + retryIn, the error text, and the lease dropped. It
// reports false when the lease is no longer held.
func (s *OutboxStore) MarkFailed(ctx context.Context, eventID uuid.UUID, token time.Time, retryIn time.Duration, cause string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE outbox_events
		SET attempts = attempts + 1,
		    next_attempt_at = now() + $3::float8 * interval '1 second',
		    last_error = $4,
		    locked_until = NULL
		WHERE event_id = $1 AND locked_until = $2 AND published_at IS NULL`,
		eventID, token, retryIn.Seconds(), cause)
	if err != nil {
		return false, classify(fmt.Errorf("postgres: mark outbox event %s failed: %w", eventID, err))
	}
	return tag.RowsAffected() == 1, nil
}

// Release drops the lease of events that were claimed but not attempted
// (shutdown, lease running out, an older event of the aggregate failed), so
// any publisher can take them at once. Attempts are unchanged. It returns
// how many leases were released.
func (s *OutboxStore) Release(ctx context.Context, eventIDs []uuid.UUID, token time.Time) (int64, error) {
	if len(eventIDs) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE outbox_events
		SET locked_until = NULL
		WHERE event_id = ANY($1) AND locked_until = $2 AND published_at IS NULL`,
		eventIDs, token)
	if err != nil {
		return 0, classify(fmt.Errorf("postgres: release outbox leases: %w", err))
	}
	return tag.RowsAffected(), nil
}

// OldestUnpublishedAge is how long the oldest unpublished event has been
// waiting (0 when every event is published): the outbox lag.
func (s *OutboxStore) OldestUnpublishedAge(ctx context.Context) (time.Duration, error) {
	var seconds float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(created_at)), 0)::float8
		FROM outbox_events WHERE published_at IS NULL`).Scan(&seconds)
	if err != nil {
		return 0, classify(fmt.Errorf("postgres: outbox lag: %w", err))
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
