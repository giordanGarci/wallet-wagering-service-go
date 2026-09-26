package sqs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/app"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/crashpoint"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// ConsumerModule runs the input queue consumer (role consumer). Its hooks
// are appended after the pool and the SQS client, so Fx stops the consumer
// before closing them.
var ConsumerModule = fx.Module("consumer",
	fx.Provide(
		app.NewConsumeWagerMessage,
		NewConsumer,
	),
	fx.Invoke(func(lc fx.Lifecycle, c *Consumer) { lc.Append(c.Hook()) }),
)

// Timeouts of the calls that settle a message (delete, DLQ send,
// visibility change). They run detached from the shutdown deadline so a
// committed message is still deleted, and a stopped one still released.
const (
	settleTimeout       = 10 * time.Second
	receiveErrorBackoff = time.Second
	maxReceiveBatch     = 10
	// correlationAttribute is the optional message attribute read when the
	// envelope has no correlationId.
	correlationAttribute = "correlationId"
)

// Result labels of wallet_sqs_messages_handled_total.
const (
	resultDuplicate    = "duplicate"
	resultReplay       = "replay"
	resultDeadLettered = "dead_lettered"
	resultRetried      = "retried"
	resultReleased     = "released"
)

// Consumer reads WagerTransactionRequested messages from the input FIFO
// queue and hands them to app.ConsumeWagerMessage.
//
//   - Long polling (CONSUMER_WAIT_TIME) for up to 10 messages, bounded by
//     free handler slots (CONSUMER_CONCURRENCY).
//   - Messages of one MessageGroupId (one Wallet) run one at a time, in
//     order; SQS does not hand out more messages of a group while one is in
//     flight, so groups run concurrently without reordering. When a message
//     is not completed, the rest of its group in the batch is released
//     (visibility 0) and SQS redelivers them after it.
//   - The message is deleted only after the commit of the operation and its
//     Inbox Message (business rejections included), or after an explicit
//     send to the DLQ for permanent errors.
//   - Transient errors set the visibility with exponential backoff by
//     ApproximateReceiveCount; the queue's redrive policy
//     (maxReceiveCount=5) moves the message to the DLQ.
//   - A heartbeat renews the visibility every VisibilityTimeout/3 while a
//     message is handled.
//   - Stop: polling ends, in-flight messages may finish until shortly
//     before the stop deadline; then they are canceled and released with
//     visibility 0, as are the received messages that never started.
type Consumer struct {
	client   *awssqs.Client
	cfg      config.Consumer
	inputURL string
	dlqURL   string
	handler  *app.ConsumeWagerMessage
	metrics  consumerMetrics
	log      *slog.Logger

	slots      chan struct{}
	soft, hard context.Context
	cancelSoft context.CancelFunc
	cancelHard context.CancelFunc
	done       chan struct{}
}

// ConsumerParams are the dependencies of NewConsumer.
type ConsumerParams struct {
	fx.In
	Client   *awssqs.Client
	Config   config.Config
	Handler  *app.ConsumeWagerMessage
	Registry prometheus.Registerer
	Log      *slog.Logger
}

// NewConsumer builds the consumer (not started).
func NewConsumer(p ConsumerParams) (*Consumer, error) {
	m, err := newConsumerMetrics(p.Registry)
	if err != nil {
		return nil, err
	}
	c := &Consumer{
		client:   p.Client,
		cfg:      p.Config.Consumer,
		inputURL: p.Config.SQS.InputQueueURL,
		dlqURL:   p.Config.SQS.DLQQueueURL,
		handler:  p.Handler,
		metrics:  m,
		log:      p.Log.With("worker", "consumer"),
		slots:    make(chan struct{}, p.Config.Consumer.Concurrency),
		done:     make(chan struct{}),
	}
	c.hard, c.cancelHard = context.WithCancel(context.Background())
	c.soft, c.cancelSoft = context.WithCancel(c.hard)
	return c, nil
}

// Hook starts and stops the consumer with the app.
func (c *Consumer) Hook() fx.Hook {
	return fx.Hook{
		OnStart: func(ctx context.Context) error {
			go c.run()
			c.log.InfoContext(ctx, "consumer started", "queue", c.inputURL,
				"concurrency", c.cfg.Concurrency, "wait_time", c.cfg.WaitTime, "visibility_timeout", c.cfg.VisibilityTimeout)
			return nil
		},
		OnStop: c.Stop,
	}
}

// Stop stops polling and waits for the in-flight messages. Shortly before
// ctx's deadline (a fifth of the remaining time, at most 3s) it cancels
// them; each canceled or never-started message is released with
// visibility 0 for another consumer.
func (c *Consumer) Stop(ctx context.Context) error {
	c.cancelSoft()
	drain := ctx
	if deadline, ok := ctx.Deadline(); ok {
		margin := min(time.Until(deadline)/5, 3*time.Second)
		var cancel context.CancelFunc
		drain, cancel = context.WithDeadline(ctx, deadline.Add(-margin))
		defer cancel()
	}
	select {
	case <-c.done:
		c.cancelHard()
		c.log.InfoContext(ctx, "consumer stopped")
		return nil
	case <-drain.Done():
	}
	c.cancelHard()
	select {
	case <-c.done:
		c.log.WarnContext(ctx, "consumer stopped at the shutdown deadline; unfinished messages released")
		return nil
	case <-ctx.Done():
		<-c.done
		return ctx.Err()
	}
}

func (c *Consumer) run() {
	defer close(c.done)
	var groups sync.WaitGroup
	defer groups.Wait()
	for {
		n := c.acquire()
		if n == 0 {
			return
		}
		msgs, err := c.receive(n)
		c.releaseSlots(n - len(msgs))
		if err != nil {
			if c.soft.Err() != nil {
				return
			}
			c.log.WarnContext(c.soft, "sqs receive failed", "error", err)
			select {
			case <-c.soft.Done():
				return
			case <-time.After(receiveErrorBackoff):
			}
			continue
		}
		for _, group := range byGroup(msgs) {
			groups.Add(1)
			go func() {
				defer groups.Done()
				c.handleGroup(group)
			}()
		}
	}
}

// acquire blocks for one free handler slot and takes up to
// maxReceiveBatch; 0 when stopping.
func (c *Consumer) acquire() int {
	select {
	case c.slots <- struct{}{}:
	case <-c.soft.Done():
		return 0
	}
	n := 1
	for n < maxReceiveBatch {
		select {
		case c.slots <- struct{}{}:
			n++
		default:
			return n
		}
	}
	return n
}

func (c *Consumer) releaseSlots(n int) {
	for range n {
		<-c.slots
	}
}

// received is one message with the time it arrived.
type received struct {
	msg types.Message
	at  time.Time
}

func (c *Consumer) receive(n int) ([]received, error) {
	out, err := c.client.ReceiveMessage(c.soft, &awssqs.ReceiveMessageInput{
		QueueUrl:            aws.String(c.inputURL),
		MaxNumberOfMessages: int32(n),
		WaitTimeSeconds:     int32(c.cfg.WaitTime / time.Second),
		VisibilityTimeout:   int32(c.cfg.VisibilityTimeout / time.Second),
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameApproximateReceiveCount,
			types.MessageSystemAttributeNameMessageGroupId,
		},
		MessageAttributeNames: []string{"All"},
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	msgs := make([]received, len(out.Messages))
	for i, m := range out.Messages {
		msgs[i] = received{msg: m, at: now}
	}
	c.metrics.received.Add(float64(len(msgs)))
	return msgs, nil
}

// byGroup splits a batch by MessageGroupId, keeping the batch order inside
// each group (SQS returns a group's messages in order).
func byGroup(msgs []received) [][]received {
	var order []string
	groups := map[string][]received{}
	for _, m := range msgs {
		g := m.msg.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], m)
	}
	out := make([][]received, len(order))
	for i, g := range order {
		out[i] = groups[g]
	}
	return out
}

// handleGroup handles a group's messages in order. It stops at the first
// message not completed (or at shutdown) and releases the rest.
func (c *Consumer) handleGroup(msgs []received) {
	for i, m := range msgs {
		if c.soft.Err() != nil {
			c.releaseAll(msgs[i:], "consumer stopping")
			return
		}
		completed := c.handle(m)
		c.releaseSlots(1)
		if !completed {
			c.releaseAll(msgs[i+1:], "earlier message of the group not completed")
			return
		}
	}
}

func (c *Consumer) releaseAll(msgs []received, why string) {
	for _, m := range msgs {
		c.changeVisibility(c.log, m.msg, 0)
		c.metrics.handled.WithLabelValues(resultReleased).Inc()
		c.log.InfoContext(c.soft, "sqs message released", "sqsMessageId", aws.ToString(m.msg.MessageId), "reason", why)
	}
	c.releaseSlots(len(msgs))
}

// outcome is what handle decided for a message.
type outcome struct {
	result string // metric label
	// exactly one of:
	complete   bool            // delete
	deadLetter *PermanentError // send to DLQ, then delete
	retry      error           // backoff visibility
	release    bool            // visibility 0 (shutdown)
}

// handle processes one message and settles it. It reports whether the
// message left the input queue.
func (c *Consumer) handle(r received) bool {
	m := r.msg
	ctx, cancel := context.WithCancel(c.hard)
	defer cancel()
	log := c.log.With("sqsMessageId", aws.ToString(m.MessageId), "receiveCount", receiveCount(m))

	stopHeartbeat := c.heartbeat(ctx, log, m)
	ctx, log, out := c.process(ctx, log, r)
	stopHeartbeat()

	settle := context.WithoutCancel(ctx)
	switch {
	case out.complete:
		crashpoint.Hit(crashpoint.ConsumerAfterCommit)
		c.delete(settle, log, m)
		c.metrics.handled.WithLabelValues(out.result).Inc()
		log.InfoContext(settle, "sqs message handled", "result", out.result)
		return true
	case out.deadLetter != nil:
		if err := c.sendToDLQ(settle, m, out.deadLetter); err != nil {
			log.WarnContext(settle, "dlq send failed; message retried", "error", err, "reason", string(out.deadLetter.Reason))
			c.retry(settle, log, m, err)
			return false
		}
		c.delete(settle, log, m)
		c.metrics.dlq.WithLabelValues(string(out.deadLetter.Reason)).Inc()
		c.metrics.handled.WithLabelValues(resultDeadLettered).Inc()
		log.WarnContext(settle, "sqs message sent to the dlq", "reason", string(out.deadLetter.Reason), "detail", out.deadLetter.Err.Error())
		return true
	case out.release:
		c.changeVisibility(log, m, 0)
		c.metrics.handled.WithLabelValues(resultReleased).Inc()
		log.WarnContext(settle, "sqs message released at shutdown")
		return false
	default:
		c.retry(settle, log, m, out.retry)
		return false
	}
}

// process parses and executes the message. It returns the context carrying
// the correlation id and log with the message identifiers added.
func (c *Consumer) process(ctx context.Context, log *slog.Logger, r received) (context.Context, *slog.Logger, outcome) {
	m := r.msg
	body := []byte(aws.ToString(m.Body))
	env, err := ParseEnvelope(body)
	if err != nil {
		var perm *PermanentError
		errors.As(err, &perm)
		return ctx, log, outcome{deadLetter: perm}
	}
	corr := env.CorrelationID
	if corr == "" {
		if a, ok := m.MessageAttributes[correlationAttribute]; ok && a.StringValue != nil && len(*a.StringValue) <= wager.MaxIdentifierLength {
			corr = *a.StringValue
		}
	}
	if corr == "" {
		corr = app.NewCorrelationID()
	}
	ctx = app.WithCorrelationID(ctx, corr)
	cmd := env.Command
	log = log.With("messageId", env.MessageID, "providerId", cmd.ProviderID(),
		"walletId", cmd.WalletID(), "externalTransactionId", cmd.ExternalTransactionID())

	res, err := c.handler.Execute(ctx, app.InboxRequest{
		ConsumerName: c.cfg.Name,
		MessageID:    env.MessageID,
		BodyHash:     BodyHash(body),
		ReceivedAt:   r.at,
		Command:      cmd,
	})
	switch {
	case err == nil && res.Duplicate:
		c.metrics.inboxDuplicates.Inc()
		t := res.Wager.Transaction
		if t != nil && t.Status() == wager.StatusFailed {
			// The first delivery was recorded FAILED; a failed DLQ send
			// brought it back. Dead-letter it again (deduplicated by SQS
			// MessageId within the FIFO window).
			return ctx, log.With("transactionId", t.ID()), failedOutcome(t)
		}
		return ctx, log, outcome{complete: true, result: resultDuplicate}
	case err == nil:
		t := res.Wager.Transaction
		log = log.With("transactionId", t.ID(), "status", string(t.Status()))
		if t.Status() == wager.StatusFailed {
			// Recorded for audit (new or Replay), but no retry fixes it.
			return ctx, log, failedOutcome(t)
		}
		result := statusResult(t.Status())
		if res.Wager.Replay {
			result = resultReplay
		}
		return ctx, log, outcome{complete: true, result: result}
	case errors.Is(err, app.ErrInboxHashMismatch):
		return ctx, log, deadLetter(ReasonBodyHashMismatch, err)
	case errors.Is(err, wager.ErrIdempotencyKeyConflict):
		return ctx, log, deadLetter(ReasonIdempotencyKeyConflict, err)
	case errors.Is(err, wager.ErrExternalTransactionConflict):
		return ctx, log, deadLetter(ReasonExternalTransactionConflict, err)
	case c.hard.Err() != nil:
		return ctx, log, outcome{release: true}
	default:
		return ctx, log, outcome{retry: err}
	}
}

// deadLetter sends the message to the DLQ for reason.
func deadLetter(reason Reason, err error) outcome {
	return outcome{deadLetter: &PermanentError{Reason: reason, Err: err}}
}

// failedOutcome dead-letters the message of an operation recorded FAILED.
func failedOutcome(t *wager.Transaction) outcome {
	return deadLetter(ReasonPermanentError, fmt.Errorf("transaction %s recorded %s %s", t.ID(), t.Status(), t.FailureCode()))
}

// statusResult is the metric label of a new operation's status.
func statusResult(s wager.Status) string {
	switch s {
	case wager.StatusProcessed:
		return "processed"
	case wager.StatusRejected:
		return "rejected"
	case wager.StatusPendingReference:
		return "pending_reference"
	case wager.StatusFailed:
		return "failed"
	default:
		return "other"
	}
}

// heartbeat renews m's visibility every VisibilityTimeout/3 until the
// returned stop function is called (which waits for it).
func (c *Consumer) heartbeat(ctx context.Context, log *slog.Logger, m types.Message) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(c.cfg.VisibilityTimeout / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			_, err := c.client.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
				QueueUrl:          aws.String(c.inputURL),
				ReceiptHandle:     m.ReceiptHandle,
				VisibilityTimeout: int32(c.cfg.VisibilityTimeout / time.Second),
			})
			if err != nil {
				if ctx.Err() == nil {
					log.WarnContext(ctx, "sqs visibility extension failed", "error", err)
				}
				continue
			}
			c.metrics.extensions.Inc()
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (c *Consumer) delete(ctx context.Context, log *slog.Logger, m types.Message) {
	ctx, cancel := context.WithTimeout(ctx, settleTimeout)
	defer cancel()
	_, err := c.client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.inputURL),
		ReceiptHandle: m.ReceiptHandle,
	})
	if err != nil {
		log.WarnContext(ctx, "sqs delete failed; the redelivery will be a duplicate", "error", err)
	}
}

// retry hides m for the backoff of its receive count; the redrive policy
// dead-letters it after maxReceiveCount receives.
func (c *Consumer) retry(ctx context.Context, log *slog.Logger, m types.Message, cause error) {
	n := receiveCount(m)
	delay := c.retryDelay(n)
	c.changeVisibility(log, m, delay)
	c.metrics.retries.Inc()
	c.metrics.handled.WithLabelValues(resultRetried).Inc()
	log.WarnContext(ctx, "sqs message will be retried", "error", cause, "retry_in", delay)
}

// retryDelay is min(RetryBase*2^(n-1), RetryMax) with jitter in [d/2, d],
// rounded up to whole seconds (SQS granularity).
func (c *Consumer) retryDelay(n int) time.Duration {
	d := c.cfg.RetryBase
	for i := 1; i < n && d < c.cfg.RetryMax; i++ {
		d *= 2
	}
	d = min(d, c.cfg.RetryMax)
	if half := d / 2; half > 0 {
		d = half + time.Duration(rand.Int64N(int64(d-half)+1))
	}
	secs := (d + time.Second - 1) / time.Second
	return max(secs, 1) * time.Second
}

func (c *Consumer) changeVisibility(log *slog.Logger, m types.Message, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	_, err := c.client.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(c.inputURL),
		ReceiptHandle:     m.ReceiptHandle,
		VisibilityTimeout: int32(d / time.Second),
	})
	if err != nil {
		log.WarnContext(ctx, "sqs visibility change failed; the visibility timeout expires on its own", "error", err)
	}
}

// sendToDLQ copies m to the dead-letter queue: same body and
// MessageGroupId, MessageDeduplicationId = the SQS MessageId (a repeated
// send after a failed delete is dropped by the DLQ), the original message
// attributes plus dlqReason, dlqDetail and dlqSourceMessageId.
func (c *Consumer) sendToDLQ(ctx context.Context, m types.Message, perm *PermanentError) error {
	ctx, cancel := context.WithTimeout(ctx, settleTimeout)
	defer cancel()
	attrs := map[string]types.MessageAttributeValue{}
	for k, v := range m.MessageAttributes {
		if len(attrs) < 7 {
			attrs[k] = v
		}
	}
	detail := perm.Err.Error()
	if len(detail) > 512 {
		detail = detail[:512]
	}
	str := func(s string) types.MessageAttributeValue {
		return types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(s)}
	}
	attrs["dlqReason"] = str(string(perm.Reason))
	attrs["dlqDetail"] = str(detail)
	attrs["dlqSourceMessageId"] = str(aws.ToString(m.MessageId))
	group := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
	if group == "" {
		group = "unknown"
	}
	_, err := c.client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(c.dlqURL),
		MessageBody:            m.Body,
		MessageGroupId:         aws.String(group),
		MessageDeduplicationId: m.MessageId,
		MessageAttributes:      attrs,
	})
	return err
}

func receiveCount(m types.Message) int {
	n, err := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	if err != nil || n < 1 {
		return 1
	}
	return n
}

type consumerMetrics struct {
	received        prometheus.Counter
	handled         *prometheus.CounterVec
	dlq             *prometheus.CounterVec
	inboxDuplicates prometheus.Counter
	retries         prometheus.Counter
	extensions      prometheus.Counter
}

func newConsumerMetrics(reg prometheus.Registerer) (consumerMetrics, error) {
	m := consumerMetrics{
		received: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_sqs_messages_received_total",
			Help: "Messages received from the input queue (redeliveries included).",
		}),
		handled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_sqs_messages_handled_total",
			Help: "Input messages by result: processed, rejected, pending_reference, failed, replay, duplicate, dead_lettered, retried, released.",
		}, []string{"result"}),
		dlq: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_sqs_dlq_messages_total",
			Help: "Input messages sent explicitly to the dead-letter queue, by reason.",
		}, []string{"reason"}),
		inboxDuplicates: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_inbox_duplicates_total",
			Help: "Redeliveries recognized by their Inbox Message (same messageId and body hash) and deleted without effect.",
		}),
		retries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_sqs_retries_total",
			Help: "Transient failures answered with ChangeMessageVisibility (exponential backoff).",
		}),
		extensions: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_sqs_visibility_extensions_total",
			Help: "Visibility timeout renewals of messages still being handled.",
		}),
	}
	for _, col := range []prometheus.Collector{m.received, m.handled, m.dlq, m.inboxDuplicates, m.retries, m.extensions} {
		if err := reg.Register(col); err != nil {
			return consumerMetrics{}, err
		}
	}
	return m, nil
}
