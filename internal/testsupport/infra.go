//go:build integration || e2e

// Package testsupport is the integration harness. It provisions isolated
// infrastructure on the running compose postgres and ministack (a database
// and three queues with a random suffix per call) and starts the Fx app
// in-process against it.
//
// Endpoints come from the environment so the harness also works from a
// container on the compose network:
//
//	TEST_DATABASE_URL      admin connection used to CREATE/DROP the per-run database
//	                       (default postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable);
//	                       its user owns the schema (runs the migrations); the app
//	                       connects to the per-run database as AppRole
//	TEST_AWS_ENDPOINT_URL  SQS endpoint (default http://localhost:4566)
//	TEST_KEYCLOAK_URL, TEST_OIDC_ISSUER_URL  Keycloak and expected issuer (see auth.go)
//	AWS_REGION, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY  (defaults us-east-1, test, test)
package testsupport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers pgx5://
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/migrations"
)

// AppRole and AppRolePassword are the least-privilege database role the
// migrations create (migrations/000006_app_role.up.sql). The app connects
// as it; the schema owner keeps running migrations and assertions.
const (
	AppRole         = "wallet_app"
	AppRolePassword = "wallet_app"
)

const (
	defaultAdminDatabaseURL = "postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable"
	defaultAWSEndpoint      = "http://localhost:4566"
	setupTimeout            = 60 * time.Second
)

// Infra is the isolated infrastructure of one test.
type Infra struct {
	// Config is valid for bootstrap.Options: it points at the isolated
	// database and queues, listens on 127.0.0.1:0 and enables APP_ROLES=api.
	Config config.Config

	DatabaseName string
	// DatabaseURL connects to the isolated database as the schema owner
	// (the TEST_DATABASE_URL user): migrations and direct SQL checks.
	DatabaseURL string
	// AppDatabaseURL connects to the isolated database as the
	// least-privilege application role (AppRole); Config.DatabaseURL uses it.
	AppDatabaseURL string
	AdminURL       string
	DLQQueueURL    string
	InputQueueURL  string
	EventsQueueURL string

	// SQS is a harness-owned client for sending to and reading from the
	// queues in tests (independent from the app's client).
	SQS *awssqs.Client

	transport *http.Transport
}

// NewInfra creates a database (migrated to the latest version) and the three
// FIFO queues, all with a random suffix, and drops them on test cleanup.
func NewInfra(t testing.TB) *Infra {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	suffix := randomSuffix(t)
	inf := &Infra{
		DatabaseName: "wallet_test_" + suffix,
		AdminURL:     env("TEST_DATABASE_URL", defaultAdminDatabaseURL),
	}

	dbURL, err := withDatabase(inf.AdminURL, inf.DatabaseName)
	if err != nil {
		t.Fatalf("testsupport: %v", err)
	}
	inf.DatabaseURL = dbURL
	if err := execAdmin(ctx, inf.AdminURL, "CREATE DATABASE "+pgx.Identifier{inf.DatabaseName}.Sanitize()); err != nil {
		t.Fatalf("testsupport: create database: %v", err)
	}
	t.Cleanup(func() { inf.dropDatabase(t) })
	if err := MigrateUp(dbURL); err != nil {
		t.Fatalf("testsupport: migrate: %v", err)
	}
	if inf.AppDatabaseURL, err = withCredentials(dbURL, AppRole, AppRolePassword); err != nil {
		t.Fatalf("testsupport: %v", err)
	}

	endpoint := env("TEST_AWS_ENDPOINT_URL", defaultAWSEndpoint)
	region := env("AWS_REGION", "us-east-1")
	inf.transport = http.DefaultTransport.(*http.Transport).Clone()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithHTTPClient(&http.Client{Transport: inf.transport}),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			env("AWS_ACCESS_KEY_ID", "test"), env("AWS_SECRET_ACCESS_KEY", "test"), "")),
	)
	if err != nil {
		t.Fatalf("testsupport: aws config: %v", err)
	}
	inf.SQS = awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) { o.BaseEndpoint = aws.String(endpoint) })
	t.Cleanup(inf.transport.CloseIdleConnections)
	if err := inf.createQueues(ctx, t, suffix); err != nil {
		t.Fatalf("testsupport: create queues: %v", err)
	}
	// Leave no idle connection behind, so leak checks start from a clean state.
	inf.transport.CloseIdleConnections()

	// The app resolves credentials through the SDK default chain.
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" {
		t.Setenv("AWS_ACCESS_KEY_ID", "test")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	}

	inf.Config, err = config.FromLookup(func(k string) (string, bool) {
		v, ok := map[string]string{
			"HTTP_ADDR":            "127.0.0.1:0",
			"DATABASE_URL":         inf.AppDatabaseURL,
			"AWS_ENDPOINT_URL":     endpoint,
			"AWS_REGION":           region,
			"SQS_INPUT_QUEUE_URL":  inf.InputQueueURL,
			"SQS_EVENTS_QUEUE_URL": inf.EventsQueueURL,
			"APP_ROLES":            "api",
			"SHUTDOWN_TIMEOUT":     "10s",
			"LOG_LEVEL":            env("TEST_LOG_LEVEL", "warn"),
			"OIDC_ISSUER_URL":      IssuerURL(),
			"OIDC_JWKS_URL":        KeycloakJWKSURL(),
		}[k]
		return v, ok
	})
	if err != nil {
		t.Fatalf("testsupport: %v", err)
	}
	return inf
}

// MigrateUp applies every embedded migration to databaseURL.
func MigrateUp(databaseURL string) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return err
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return err
	}
	u.Scheme = "pgx5"
	m, err := migrate.NewWithSourceInstance("iofs", src, u.String())
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// DropDatabase drops the isolated database, terminating open connections.
// Tests use it to simulate PostgreSQL going away; cleanup tolerates it.
func (i *Infra) DropDatabase(ctx context.Context) error {
	return execAdmin(ctx, i.AdminURL,
		"DROP DATABASE IF EXISTS "+pgx.Identifier{i.DatabaseName}.Sanitize()+" WITH (FORCE)")
}

func (i *Infra) dropDatabase(t testing.TB) {
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	if err := i.DropDatabase(ctx); err != nil {
		t.Errorf("testsupport: drop database %s: %v", i.DatabaseName, err)
	}
}

func (i *Infra) createQueues(ctx context.Context, t testing.TB, suffix string) error {
	create := func(name string, attrs map[string]string) (string, error) {
		attrs["FifoQueue"] = "true"
		out, err := i.SQS.CreateQueue(ctx, &awssqs.CreateQueueInput{QueueName: aws.String(name), Attributes: attrs})
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		queueURL := aws.ToString(out.QueueUrl)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
			defer cancel()
			_, err := i.SQS.DeleteQueue(ctx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
			var gone *types.QueueDoesNotExist
			if err != nil && !errors.As(err, &gone) {
				t.Logf("testsupport: delete queue %s: %v", name, err)
			}
		})
		return queueURL, nil
	}

	var err error
	if i.DLQQueueURL, err = create("wager-transactions-dlq-"+suffix+".fifo", map[string]string{}); err != nil {
		return err
	}
	attrs, err := i.SQS.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(i.DLQQueueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return fmt.Errorf("dlq arn: %w", err)
	}
	redrive := fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":"5"}`, attrs.Attributes[string(types.QueueAttributeNameQueueArn)])
	if i.InputQueueURL, err = create("wager-transactions-"+suffix+".fifo", map[string]string{
		"VisibilityTimeout":             "30",
		"ReceiveMessageWaitTimeSeconds": "20",
		"RedrivePolicy":                 redrive,
	}); err != nil {
		return err
	}
	i.EventsQueueURL, err = create("wallet-events-"+suffix+".fifo", map[string]string{})
	return err
}

func execAdmin(ctx context.Context, adminURL, sql string) error {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	_, err = conn.Exec(ctx, sql)
	return err
}

// withCredentials returns rawURL with its user and password replaced.
func withCredentials(rawURL, user, password string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("database url: %w", err)
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}

// OpenPool connects a pgx pool to databaseURL, closed on test cleanup
// (before the isolated database is dropped).
func OpenPool(t testing.TB, databaseURL string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("testsupport: open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func withDatabase(rawURL, name string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("TEST_DATABASE_URL: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

func randomSuffix(t testing.TB) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("testsupport: random suffix: %v", err)
	}
	return hex.EncodeToString(b)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
