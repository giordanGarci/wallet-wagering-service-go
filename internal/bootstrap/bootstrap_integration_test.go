//go:build integration

package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/goleak"

	"github.com/junglegaming/backend-challenge-go/internal/bootstrap"
	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

type readiness struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func getReady(t *testing.T, app *testsupport.App) (int, readiness) {
	t.Helper()
	code, body := app.Get(t, "/health/ready")
	var r readiness
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("ready body %q: %v", body, err)
	}
	return code, r
}

// TestAppStartsServesAndStopsCleanly covers the Fx composition end to end:
// start against real PostgreSQL and SQS, serve the operational endpoints,
// stop, and leave no goroutine or pool connection behind.
func TestAppStartsServesAndStopsCleanly(t *testing.T) {
	infra := testsupport.NewInfra(t)
	baseline := goleak.IgnoreCurrent()

	app := testsupport.StartApp(t, infra.Config)

	if code, _ := app.Get(t, "/health/live"); code != http.StatusOK {
		t.Fatalf("/health/live = %d, want 200", code)
	}
	code, ready := getReady(t, app)
	if code != http.StatusOK || ready.Status != "ok" || ready.Checks["postgres"] != "up" || ready.Checks["sqs"] != "up" {
		t.Fatalf("/health/ready = %d %+v, want 200 with postgres and sqs up", code, ready)
	}
	code, metrics := app.Get(t, "/metrics")
	if code != http.StatusOK || !strings.Contains(string(metrics), "go_goroutines") {
		t.Fatalf("/metrics = %d, want 200 exposing go_goroutines", code)
	}

	pool := app.Pool
	app.Stop()

	if err := pool.Ping(context.Background()); err == nil {
		t.Error("pool still usable after stop, want it closed")
	}
	if n := pool.Stat().TotalConns(); n != 0 {
		t.Errorf("pool has %d connections after stop, want 0", n)
	}
	goleak.VerifyNone(t, baseline)
}

func TestReadyReportsSQSDown(t *testing.T) {
	infra := testsupport.NewInfra(t)
	app := testsupport.StartApp(t, infra.Config)

	_, err := infra.SQS.DeleteQueue(context.Background(), &awssqs.DeleteQueueInput{QueueUrl: aws.String(infra.InputQueueURL)})
	if err != nil {
		t.Fatalf("delete input queue: %v", err)
	}

	code, ready := getReady(t, app)
	if code != http.StatusServiceUnavailable || ready.Checks["sqs"] != "down" || ready.Checks["postgres"] != "up" {
		t.Fatalf("/health/ready = %d %+v, want 503 with sqs down", code, ready)
	}
}

func TestReadyReportsPostgresDown(t *testing.T) {
	infra := testsupport.NewInfra(t)
	app := testsupport.StartApp(t, infra.Config)

	if err := infra.DropDatabase(context.Background()); err != nil {
		t.Fatalf("drop database: %v", err)
	}

	code, ready := getReady(t, app)
	if code != http.StatusServiceUnavailable || ready.Checks["postgres"] != "down" || ready.Checks["sqs"] != "up" {
		t.Fatalf("/health/ready = %d %+v, want 503 with postgres down", code, ready)
	}
}

// TestStartFailsFastWhenDependencyIsMissing: a queue that does not exist
// aborts startup instead of serving traffic.
func TestStartFailsFastWhenDependencyIsMissing(t *testing.T) {
	infra := testsupport.NewInfra(t)
	cfg := infra.Config
	cfg.SQS.InputQueueURL = strings.Replace(cfg.SQS.InputQueueURL, "wager-transactions-", "missing-", 1)

	app := bootstrap.New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := app.Start(ctx)
	if err == nil {
		_ = app.Stop(ctx)
		t.Fatal("start succeeded with a missing queue, want an error")
	}
	if !strings.Contains(err.Error(), "sqs") {
		t.Errorf("start error %q does not name sqs", err)
	}
}

// TestStartFailsFastWhenJWKSIsUnreachable: without signing keys the API
// could only answer 401, so startup aborts instead.
func TestStartFailsFastWhenJWKSIsUnreachable(t *testing.T) {
	infra := testsupport.NewInfra(t)
	cfg := infra.Config
	cfg.OIDC.JWKSURL = "http://127.0.0.1:1/realms/wallet/protocol/openid-connect/certs"

	app := bootstrap.New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := app.Start(ctx)
	if err == nil {
		_ = app.Stop(ctx)
		t.Fatal("start succeeded with an unreachable JWKS, want an error")
	}
	if !strings.Contains(err.Error(), "oidc") {
		t.Errorf("start error %q does not name oidc", err)
	}
}
