// Package sqs owns the AWS SQS client and, in later tickets, the input queue
// consumer and the outbox publisher.
package sqs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/health"
)

// Module provides *awssqs.Client and the "sqs" readiness check.
//
// The client uses its own HTTP transport, whose idle connections are closed
// on stop. OnStart verifies that every configured queue exists.
var Module = fx.Module("sqs",
	fx.Provide(
		NewClient,
		health.AsCheck(NewReadinessCheck),
	),
	fx.Invoke(registerStartupCheck),
)

// NewClient builds the SQS client for cfg.AWS and registers the hook that
// releases its connections.
func NewClient(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) (*awssqs.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.AWS.Region),
		awsconfig.WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		return nil, fmt.Errorf("sqs: load AWS config: %w", err)
	}
	client := awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
		if cfg.AWS.EndpointURL != "" {
			o.BaseEndpoint = aws.String(cfg.AWS.EndpointURL)
		}
	})
	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			transport.CloseIdleConnections()
			log.InfoContext(ctx, "sqs client closed")
			return nil
		},
	})
	return client, nil
}

// NewReadinessCheck verifies that the input and events queues are reachable.
func NewReadinessCheck(client *awssqs.Client, cfg config.Config) health.Check {
	queues := []string{cfg.SQS.InputQueueURL, cfg.SQS.EventsQueueURL}
	return health.Check{
		Name: "sqs",
		Check: func(ctx context.Context) error {
			var errs []error
			for _, url := range queues {
				_, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
					QueueUrl:       aws.String(url),
					AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
				})
				if err != nil {
					errs = append(errs, fmt.Errorf("queue %s: %w", url, err))
				}
			}
			return errors.Join(errs...)
		},
	}
}

type startupCheckParams struct {
	fx.In
	Lifecycle fx.Lifecycle
	Client    *awssqs.Client
	Config    config.Config
	Log       *slog.Logger
}

func registerStartupCheck(p startupCheckParams) {
	check := NewReadinessCheck(p.Client, p.Config)
	p.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := check.Check(ctx); err != nil {
				return fmt.Errorf("sqs: %w", err)
			}
			p.Log.InfoContext(ctx, "sqs queues reachable")
			return nil
		},
	})
}
