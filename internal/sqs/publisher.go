package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/junglegaming/backend-challenge-go/internal/config"
)

// EventPublisher sends Outbox Events to the events FIFO queue
// (SQS_EVENTS_QUEUE_URL).
type EventPublisher struct {
	client   *awssqs.Client
	queueURL string
}

// NewEventPublisher binds the publisher to the configured events queue.
func NewEventPublisher(client *awssqs.Client, cfg config.Config) *EventPublisher {
	return &EventPublisher{client: client, queueURL: cfg.SQS.EventsQueueURL}
}

// Publish sends body verbatim with MessageGroupId = groupID (the aggregate,
// so a Wallet's events stay ordered) and MessageDeduplicationId =
// deduplicationID (the eventId, so a republication inside the SQS
// deduplication window is dropped by the queue).
func (p *EventPublisher) Publish(ctx context.Context, groupID, deduplicationID, body string) error {
	_, err := p.client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(p.queueURL),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(deduplicationID),
	})
	if err != nil {
		return fmt.Errorf("sqs: send event %s: %w", deduplicationID, err)
	}
	return nil
}
