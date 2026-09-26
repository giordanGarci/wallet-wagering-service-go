//go:build integration

package worker_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/goleak"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
	"github.com/junglegaming/backend-challenge-go/internal/worker"
)

// outboxEnv is an isolated database and queues plus an owner pool for
// seeding and asserting outbox rows. Apps are started per test.
type outboxEnv struct {
	infra *testsupport.Infra
	db    *pgxpool.Pool
	cfg   config.Config
}

func newOutboxEnv(t *testing.T, roles ...config.Role) *outboxEnv {
	t.Helper()
	infra := testsupport.NewInfra(t)
	cfg := infra.Config
	if len(roles) == 0 {
		roles = []config.Role{config.RoleOutbox}
	}
	cfg.Roles = roles
	cfg.Outbox = config.Outbox{
		PollInterval: 20 * time.Millisecond,
		BatchSize:    10,
		Lease:        5 * time.Second,
		RetryBase:    200 * time.Millisecond,
		RetryMax:     time.Second,
	}
	return &outboxEnv{infra: infra, db: testsupport.OpenPool(t, infra.DatabaseURL), cfg: cfg}
}

type seeded struct {
	EventID     uuid.UUID
	AggregateID uuid.UUID
	Payload     string
}

// seed inserts n events of aggregate as the owner, in seq order.
func (e *outboxEnv) seed(t *testing.T, aggregate uuid.UUID, n int) []seeded {
	t.Helper()
	var out []seeded
	for i := range n {
		id := uuid.Must(uuid.NewV7())
		// Unusual spacing and key order: the published body must be these
		// exact bytes, not a re-encoding.
		payload := fmt.Sprintf(`{"eventId": "%s",  "aggregateId":"%s", "n": %d}`, id, aggregate, i)
		_, err := e.db.Exec(context.Background(), `
			INSERT INTO outbox_events (event_id, aggregate_id, event_type, version, correlation_id, payload, occurred_at)
			VALUES ($1, $2, 'TestEvent', 1, 'corr-test', $3, now())`, id, aggregate, payload)
		if err != nil {
			t.Fatalf("seed outbox event: %v", err)
		}
		out = append(out, seeded{EventID: id, AggregateID: aggregate, Payload: payload})
	}
	return out
}

func (e *outboxEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (e *outboxEnv) waitAllPublished(t *testing.T, timeout time.Duration) {
	t.Helper()
	eventually(t, timeout, "every outbox event published", func() bool {
		return e.count(t, "SELECT count(*) FROM outbox_events WHERE published_at IS NULL") == 0
	})
}

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for: %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type received struct {
	Body            string
	GroupID         string
	DeduplicationID string
}

// receive reads (and deletes) messages from the events queue until want
// arrived, then keeps reading briefly to catch extra ones.
func (e *outboxEnv) receive(t *testing.T, want int) []received {
	t.Helper()
	ctx := context.Background()
	var out []received
	deadline := time.Now().Add(30 * time.Second)
	extra := time.Time{}
	for time.Now().Before(deadline) {
		if len(out) >= want {
			if extra.IsZero() {
				extra = time.Now().Add(time.Second)
			} else if time.Now().After(extra) {
				break
			}
		}
		resp, err := e.infra.SQS.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(e.infra.EventsQueueURL),
			MaxNumberOfMessages:         10,
			WaitTimeSeconds:             1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
		})
		if err != nil {
			t.Fatalf("receive events: %v", err)
		}
		for _, m := range resp.Messages {
			out = append(out, received{
				Body:            aws.ToString(m.Body),
				GroupID:         m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
				DeduplicationID: m.Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)],
			})
			if _, err := e.infra.SQS.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
				QueueUrl: aws.String(e.infra.EventsQueueURL), ReceiptHandle: m.ReceiptHandle,
			}); err != nil {
				t.Fatalf("delete event: %v", err)
			}
		}
	}
	if len(out) != want {
		t.Fatalf("received %d events, want %d", len(out), want)
	}
	return out
}

// countingPublisher wraps the real publisher and counts sends per eventId,
// because the FIFO queue's deduplication would hide a duplicate send.
type countingPublisher struct {
	mu    sync.Mutex
	sends map[string]int
}

func (c *countingPublisher) decorate(next worker.Publisher) worker.Publisher {
	return publisherFunc(func(ctx context.Context, group, dedup, body string) error {
		err := next.Publish(ctx, group, dedup, body)
		if err == nil {
			c.mu.Lock()
			c.sends[dedup]++
			c.mu.Unlock()
		}
		return err
	})
}

type publisherFunc func(ctx context.Context, group, dedup, body string) error

func (f publisherFunc) Publish(ctx context.Context, group, dedup, body string) error {
	return f(ctx, group, dedup, body)
}

func TestPublishesOpeningEventsVerbatimAfterCommit(t *testing.T) {
	e := newOutboxEnv(t, config.RoleAPI, config.RoleOutbox)
	app := testsupport.StartApp(t, e.cfg)

	playerID := uuid.New()
	resp, body := app.Do(t, http.MethodPost, "/wallets", testsupport.Token(t, testsupport.InternalClient),
		`{"playerId":"`+playerID.String()+`","initialBalance":{"amount":"10.00","currency":"BRL"}}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open wallet = %d %s", resp.StatusCode, body)
	}
	var walletID uuid.UUID
	if err := e.db.QueryRow(context.Background(), "SELECT id FROM wallets WHERE player_id = $1", playerID).Scan(&walletID); err != nil {
		t.Fatalf("wallet id: %v", err)
	}

	msgs := e.receive(t, 2)
	e.waitAllPublished(t, 10*time.Second)

	rows, err := e.db.Query(context.Background(), `
		SELECT event_id, payload, attempts, locked_until IS NULL, published_at IS NOT NULL
		FROM outbox_events WHERE aggregate_id = $1 ORDER BY seq`, walletID)
	if err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var (
			id                 uuid.UUID
			payload            string
			attempts           int
			unlocked, publishd bool
		)
		if err := rows.Scan(&id, &payload, &attempts, &unlocked, &publishd); err != nil {
			t.Fatalf("scan: %v", err)
		}
		m := msgs[i]
		if m.Body != payload {
			t.Errorf("event %d body = %s, want the stored payload %s", i, m.Body, payload)
		}
		if m.GroupID != walletID.String() || m.DeduplicationID != id.String() {
			t.Errorf("event %d group/dedup = %s/%s, want %s/%s", i, m.GroupID, m.DeduplicationID, walletID, id)
		}
		if attempts != 0 || !unlocked || !publishd {
			t.Errorf("event %d row: attempts=%d unlocked=%v published=%v", i, attempts, unlocked, publishd)
		}
		i++
	}
	if i != 2 {
		t.Fatalf("outbox rows = %d, want 2", i)
	}

	_, metrics := app.Get(t, "/metrics")
	for _, want := range []string{"wallet_outbox_published_total 2", "wallet_outbox_lag_seconds 0", "wallet_outbox_publish_failures_total 0"} {
		eventually(t, 5*time.Second, "metric "+want, func() bool {
			_, metrics = app.Get(t, "/metrics")
			return strings.Contains(string(metrics), want)
		})
	}
}

func TestConcurrentPublishersPublishEachEventOnce(t *testing.T) {
	e := newOutboxEnv(t)
	e.cfg.Outbox.BatchSize = 7
	var all []seeded
	for range 10 {
		all = append(all, e.seed(t, uuid.New(), 10)...)
	}

	counter := &countingPublisher{sends: map[string]int{}}
	testsupport.StartApp(t, e.cfg, fx.Decorate(counter.decorate))
	testsupport.StartApp(t, e.cfg, fx.Decorate(counter.decorate))
	e.waitAllPublished(t, 30*time.Second)

	counter.mu.Lock()
	for _, ev := range all {
		if n := counter.sends[ev.EventID.String()]; n != 1 {
			t.Errorf("event %s sent %d times, want 1", ev.EventID, n)
		}
	}
	if len(counter.sends) != len(all) {
		t.Errorf("sent %d distinct events, want %d", len(counter.sends), len(all))
	}
	counter.mu.Unlock()

	msgs := e.receive(t, len(all))
	byGroup := map[string][]string{}
	seen := map[string]bool{}
	for _, m := range msgs {
		if seen[m.DeduplicationID] {
			t.Errorf("event %s received twice", m.DeduplicationID)
		}
		seen[m.DeduplicationID] = true
		byGroup[m.GroupID] = append(byGroup[m.GroupID], m.Body)
	}
	// FIFO per aggregate: each group arrives in seq (insertion) order.
	want := map[string][]string{}
	for _, ev := range all {
		want[ev.AggregateID.String()] = append(want[ev.AggregateID.String()], ev.Payload)
	}
	for g, bodies := range want {
		if !slices.Equal(byGroup[g], bodies) {
			t.Errorf("group %s received out of order or incomplete:\n got %v\nwant %v", g, byGroup[g], bodies)
		}
	}
}

func TestAbandonedLeaseIsTakenOverWithTheSameEventID(t *testing.T) {
	e := newOutboxEnv(t)
	dead := e.seed(t, uuid.New(), 1)[0]
	live := e.seed(t, uuid.New(), 1)[0]
	ctx := context.Background()
	// A publisher died holding dead's lease (expired); another one still
	// holds live's lease for 3s.
	if _, err := e.db.Exec(ctx, "UPDATE outbox_events SET locked_until = now() - interval '1 second' WHERE event_id = $1", dead.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, "UPDATE outbox_events SET locked_until = now() + interval '3 seconds' WHERE event_id = $1", live.EventID); err != nil {
		t.Fatal(err)
	}

	testsupport.StartApp(t, e.cfg)
	eventually(t, 10*time.Second, "abandoned event published", func() bool {
		return e.count(t, "SELECT count(*) FROM outbox_events WHERE event_id = $1 AND published_at IS NOT NULL", dead.EventID) == 1
	})
	if n := e.count(t, "SELECT count(*) FROM outbox_events WHERE event_id = $1 AND published_at IS NULL", live.EventID); n != 1 {
		t.Fatal("event under a live lease was published before the lease expired")
	}
	e.waitAllPublished(t, 10*time.Second)

	msgs := e.receive(t, 2)
	if msgs[0].DeduplicationID != dead.EventID.String() || msgs[0].Body != dead.Payload {
		t.Errorf("first message = %+v, want the abandoned event %s", msgs[0], dead.EventID)
	}
	if msgs[1].DeduplicationID != live.EventID.String() || msgs[1].Body != live.Payload {
		t.Errorf("second message = %+v, want the leased event %s", msgs[1], live.EventID)
	}
	if n := e.count(t, "SELECT count(*) FROM outbox_events WHERE attempts = 0 AND locked_until IS NULL"); n != 2 {
		t.Errorf("rows with attempts 0 and no lease = %d, want 2", n)
	}
}

func TestSQSUnavailableRetriesWithBackoffAndPublishesLater(t *testing.T) {
	e := newOutboxEnv(t)
	e.cfg.Outbox.RetryBase = 2 * time.Second
	e.cfg.Outbox.RetryMax = 2 * time.Second
	app := testsupport.StartApp(t, e.cfg)
	ctx := context.Background()

	// The events queue disappears after startup.
	if _, err := e.infra.SQS.DeleteQueue(ctx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(e.infra.EventsQueueURL)}); err != nil {
		t.Fatalf("delete events queue: %v", err)
	}
	aggregate := uuid.New()
	evs := e.seed(t, aggregate, 2)

	eventually(t, 15*time.Second, "failed attempt recorded", func() bool {
		return e.count(t, "SELECT count(*) FROM outbox_events WHERE event_id = $1 AND attempts >= 1", evs[0].EventID) == 1
	})
	var (
		scheduledAhead bool
		lastError      string
		locked         bool
	)
	if err := e.db.QueryRow(ctx, `SELECT next_attempt_at > now(), last_error, locked_until IS NOT NULL
		FROM outbox_events WHERE event_id = $1`, evs[0].EventID).Scan(&scheduledAhead, &lastError, &locked); err != nil {
		t.Fatal(err)
	}
	if !scheduledAhead || lastError == "" || locked {
		t.Errorf("after a failure: next_attempt_at in the future=%v, last_error=%q, still leased=%v", scheduledAhead, lastError, locked)
	}
	// The second event of the aggregate waits behind the first one.
	if n := e.count(t, "SELECT count(*) FROM outbox_events WHERE event_id = $1 AND published_at IS NULL", evs[1].EventID); n != 1 {
		t.Error("a later event of the aggregate was published before the failed one")
	}

	name := e.infra.EventsQueueURL[strings.LastIndex(e.infra.EventsQueueURL, "/")+1:]
	out, err := e.infra.SQS.CreateQueue(ctx, &awssqs.CreateQueueInput{
		QueueName: aws.String(name), Attributes: map[string]string{"FifoQueue": "true"},
	})
	if err != nil || aws.ToString(out.QueueUrl) != e.infra.EventsQueueURL {
		t.Fatalf("recreate events queue: %v (url %s)", err, aws.ToString(out.QueueUrl))
	}

	e.waitAllPublished(t, 20*time.Second)
	msgs := e.receive(t, 2)
	for i, m := range msgs {
		if m.DeduplicationID != evs[i].EventID.String() || m.Body != evs[i].Payload {
			t.Errorf("message %d = %+v, want event %s", i, m, evs[i].EventID)
		}
	}
	_, metrics := app.Get(t, "/metrics")
	if strings.Contains(string(metrics), "wallet_outbox_publish_failures_total 0") ||
		strings.Contains(string(metrics), "wallet_outbox_retries_total 0") {
		t.Errorf("metrics do not report the failures and retries:\n%s", metrics)
	}
}

func TestShutdownMidBatchFinishesTheSendAndReleasesTheRest(t *testing.T) {
	e := newOutboxEnv(t)
	evs := e.seed(t, uuid.New(), 5)

	started := make(chan struct{})
	unblock := make(chan struct{})
	var once sync.Once
	block := func(next worker.Publisher) worker.Publisher {
		return publisherFunc(func(ctx context.Context, group, dedup, body string) error {
			once.Do(func() {
				close(started)
				<-unblock
			})
			return next.Publish(ctx, group, dedup, body)
		})
	}
	app := testsupport.StartApp(t, e.cfg, fx.Decorate(block))
	<-started

	stopped := make(chan struct{})
	go func() {
		app.Stop()
		close(stopped)
	}()
	time.Sleep(200 * time.Millisecond) // the stop signal reaches the worker
	close(unblock)
	select {
	case <-stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("app did not stop")
	}

	if n := e.count(t, "SELECT count(*) FROM outbox_events WHERE event_id = $1 AND published_at IS NOT NULL", evs[0].EventID); n != 1 {
		t.Error("the send in flight at shutdown was not recorded as published")
	}
	if n := e.count(t, "SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND locked_until IS NULL AND attempts = 0"); n != 4 {
		t.Errorf("released unpublished events = %d, want 4", n)
	}
	e.receive(t, 1)
}

func TestOutboxWorkerStopsWithoutLeaks(t *testing.T) {
	e := newOutboxEnv(t)
	e.seed(t, uuid.New(), 3)
	baseline := goleak.IgnoreCurrent()

	app := testsupport.StartApp(t, e.cfg)
	e.waitAllPublished(t, 10*time.Second)
	app.Stop()

	goleak.VerifyNone(t, baseline)
}
