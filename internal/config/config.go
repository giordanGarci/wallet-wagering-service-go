// Package config loads and validates the service configuration from
// environment variables. Validation runs before the Fx graph is built, so an
// invalid environment aborts startup with a single, explicit error.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"
)

// Role is a component an instance may run. Any instance can run any subset.
type Role string

const (
	RoleAPI      Role = "api"
	RoleConsumer Role = "consumer"
	RoleOutbox   Role = "outbox"
	RolePending  Role = "pending"
)

// KnownRoles lists every accepted APP_ROLES value, in canonical order.
var KnownRoles = []Role{RoleAPI, RoleConsumer, RoleOutbox, RolePending}

// Defaults applied when the variable is unset or empty.
const (
	DefaultHTTPAddr        = ":8080"
	DefaultAWSRegion       = "us-east-1"
	DefaultRoles           = "api,consumer,outbox,pending"
	DefaultShutdownTimeout = 25 * time.Second
	DefaultLogLevel        = "info"
)

// Config is the validated service configuration.
type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	AWS             AWS
	SQS             SQS
	Roles           []Role
	ShutdownTimeout time.Duration
	LogLevel        slog.Level
}

// AWS holds the AWS SDK settings. Credentials are resolved by the SDK default
// chain (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY).
type AWS struct {
	// EndpointURL overrides the service endpoint (MiniStack locally). Empty
	// means the real AWS endpoint for Region.
	EndpointURL string
	Region      string
}

// SQS holds the URLs of the queues the service talks to.
type SQS struct {
	InputQueueURL  string
	EventsQueueURL string
}

// HasRole reports whether r is enabled in this instance.
func (c Config) HasRole(r Role) bool {
	for _, have := range c.Roles {
		if have == r {
			return true
		}
	}
	return false
}

// Load reads the configuration from the process environment.
func Load() (Config, error) {
	return FromLookup(os.LookupEnv)
}

// FromLookup reads the configuration through lookup (os.LookupEnv in
// production, a map in tests) and validates it. Every problem found is
// reported in the returned error, not only the first.
func FromLookup(lookup func(string) (string, bool)) (Config, error) {
	get := func(key, def string) string {
		if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}

	var errs []error
	cfg := Config{
		HTTPAddr:    get("HTTP_ADDR", DefaultHTTPAddr),
		DatabaseURL: get("DATABASE_URL", ""),
		AWS: AWS{
			EndpointURL: get("AWS_ENDPOINT_URL", ""),
			Region:      get("AWS_REGION", DefaultAWSRegion),
		},
		SQS: SQS{
			InputQueueURL:  get("SQS_INPUT_QUEUE_URL", ""),
			EventsQueueURL: get("SQS_EVENTS_QUEUE_URL", ""),
		},
		ShutdownTimeout: DefaultShutdownTimeout,
	}

	roles, err := parseRoles(get("APP_ROLES", DefaultRoles))
	if err != nil {
		errs = append(errs, err)
	}
	cfg.Roles = roles

	if raw := get("SHUTDOWN_TIMEOUT", ""); raw != "" {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("SHUTDOWN_TIMEOUT: %q is not a duration (e.g. 25s)", raw))
		case d <= 0:
			errs = append(errs, fmt.Errorf("SHUTDOWN_TIMEOUT: must be positive, got %s", raw))
		default:
			cfg.ShutdownTimeout = d
		}
	}

	if raw := get("LOG_LEVEL", DefaultLogLevel); cfg.LogLevel.UnmarshalText([]byte(raw)) != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %q is not one of debug, info, warn, error", raw))
	}

	if !strings.Contains(cfg.HTTPAddr, ":") {
		errs = append(errs, fmt.Errorf("HTTP_ADDR: %q must be host:port or :port", cfg.HTTPAddr))
	}
	errs = append(errs,
		validateURL("DATABASE_URL", cfg.DatabaseURL, true, "postgres", "postgresql"),
		validateURL("AWS_ENDPOINT_URL", cfg.AWS.EndpointURL, false, "http", "https"),
		validateURL("SQS_INPUT_QUEUE_URL", cfg.SQS.InputQueueURL, true, "http", "https"),
		validateURL("SQS_EVENTS_QUEUE_URL", cfg.SQS.EventsQueueURL, true, "http", "https"),
	)

	var problems []string
	for _, err := range errs {
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}
	return cfg, nil
}

func parseRoles(raw string) ([]Role, error) {
	seen := map[Role]bool{}
	var roles []Role
	for _, part := range strings.Split(raw, ",") {
		r := Role(strings.ToLower(strings.TrimSpace(part)))
		if r == "" {
			continue
		}
		if !isKnownRole(r) {
			return nil, fmt.Errorf("APP_ROLES: unknown role %q (known: api, consumer, outbox, pending)", r)
		}
		if seen[r] {
			return nil, fmt.Errorf("APP_ROLES: role %q listed twice", r)
		}
		seen[r] = true
		roles = append(roles, r)
	}
	if len(roles) == 0 {
		return nil, errors.New("APP_ROLES: at least one role is required")
	}
	return roles, nil
}

func isKnownRole(r Role) bool {
	for _, k := range KnownRoles {
		if k == r {
			return true
		}
	}
	return false
}

// validateURL returns nil when value is an absolute URL with one of schemes,
// or when it is empty and optional. The value itself is never echoed because
// it may carry credentials.
func validateURL(key, value string, required bool, schemes ...string) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s: required", key)
		}
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s: not a valid URL", key)
	}
	for _, s := range schemes {
		if u.Scheme == s {
			return nil
		}
	}
	return fmt.Errorf("%s: scheme must be one of %s", key, strings.Join(schemes, ", "))
}
