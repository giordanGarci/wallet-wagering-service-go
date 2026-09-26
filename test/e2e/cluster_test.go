//go:build e2e

package e2e_test

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junglegaming/backend-challenge-go/internal/testsupport"
)

// Instances is the number of app processes a Cluster starts.
const Instances = 3

const (
	readyTimeout = 60 * time.Second
	stopTimeout  = 30 * time.Second
)

// baseEnv holds the worker settings every instance gets unless overridden:
// short intervals so asynchronous work finishes within the test deadlines,
// and a pending window (PENDING_MAX_ATTEMPTS=1000, retries capped at 500ms)
// long enough to survive a full restart.
var baseEnv = map[string]string{
	"APP_ROLES":              "api,consumer,outbox,pending",
	"SHUTDOWN_TIMEOUT":       "10s",
	"OUTBOX_POLL_INTERVAL":   "100ms",
	"OUTBOX_RETRY_BASE":      "100ms",
	"OUTBOX_RETRY_MAX":       "1s",
	"OUTBOX_LEASE_DURATION":  "5s",
	"PENDING_POLL_INTERVAL":  "100ms",
	"PENDING_RETRY_BASE":     "100ms",
	"PENDING_RETRY_MAX":      "500ms",
	"PENDING_LEASE_DURATION": "3s",
	"PENDING_MAX_ATTEMPTS":   "1000",
	// Short long-poll so a stop does not wait for a 20s ReceiveMessage.
	"CONSUMER_WAIT_TIME":  "1s",
	"CONSUMER_RETRY_BASE": "100ms",
	"CONSUMER_RETRY_MAX":  "1s",
}

// Cluster is one isolated infrastructure (database + queues) shared by
// several app processes.
type Cluster struct {
	Infra *testsupport.Infra
	// DB is an owner pool on the isolated database, for SQL assertions.
	DB *pgxpool.Pool
	// HTTP is shared by every request the test sends to any instance.
	HTTP *http.Client

	env  map[string]string
	mu   sync.Mutex
	live []*Instance
	seq  int
	next atomic.Uint64
}

// NewCluster provisions the infrastructure and starts Instances processes
// with env = infra endpoints + baseEnv + overrides.
func NewCluster(t *testing.T, overrides map[string]string) *Cluster {
	t.Helper()
	c := NewStoppedCluster(t, overrides)
	c.StartAll(t, Instances, nil)
	return c
}

// NewStoppedCluster is NewCluster without starting any instance.
func NewStoppedCluster(t *testing.T, overrides map[string]string) *Cluster {
	t.Helper()
	inf := testsupport.NewInfra(t)
	c := &Cluster{
		Infra: inf,
		DB:    testsupport.OpenPool(t, inf.DatabaseURL),
		HTTP: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{MaxIdleConnsPerHost: 64},
		},
		env: map[string]string{
			"DATABASE_URL":          inf.AppDatabaseURL,
			"AWS_ENDPOINT_URL":      inf.Config.AWS.EndpointURL,
			"AWS_REGION":            inf.Config.AWS.Region,
			"AWS_ACCESS_KEY_ID":     envOr("AWS_ACCESS_KEY_ID", "test"),
			"AWS_SECRET_ACCESS_KEY": envOr("AWS_SECRET_ACCESS_KEY", "test"),
			"SQS_INPUT_QUEUE_URL":   inf.InputQueueURL,
			"SQS_EVENTS_QUEUE_URL":  inf.EventsQueueURL,
			"SQS_DLQ_QUEUE_URL":     inf.DLQQueueURL,
			"OIDC_ISSUER_URL":       testsupport.IssuerURL(),
			"OIDC_JWKS_URL":         testsupport.KeycloakJWKSURL(),
			"LOG_LEVEL":             envOr("TEST_LOG_LEVEL", "info"),
		},
	}
	for k, v := range baseEnv {
		c.env[k] = v
	}
	for k, v := range overrides {
		c.env[k] = v
	}
	t.Cleanup(func() { c.HTTP.CloseIdleConnections() })
	return c
}

// StartAll starts n instances in parallel (each with extra env) and waits
// until all are ready.
func (c *Cluster) StartAll(t *testing.T, n int, extra map[string]string) []*Instance {
	t.Helper()
	out := make([]*Instance, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		inst, err := c.launch(t, extra)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = inst
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = inst.waitReady(c.HTTP, readyTimeout)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("start instances: %v", err)
	}
	return out
}

// Start starts one instance with extra env on top of the cluster env (e.g.
// {"CRASH_AT": "outbox.before_publish"}) and waits until /health/ready
// answers 200. Use Launch when the instance may exit before being ready.
func (c *Cluster) Start(t *testing.T, extra map[string]string) *Instance {
	t.Helper()
	inst := c.Launch(t, extra)
	if err := inst.waitReady(c.HTTP, readyTimeout); err != nil {
		t.Fatal(err)
	}
	return inst
}

// Launch starts one instance without waiting for readiness.
func (c *Cluster) Launch(t *testing.T, extra map[string]string) *Instance {
	t.Helper()
	inst, err := c.launch(t, extra)
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func (c *Cluster) launch(t *testing.T, extra map[string]string) (*Instance, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.seq++
	name := fmt.Sprintf("app-%d", c.seq)
	c.mu.Unlock()

	env := os.Environ()
	for k, v := range c.env {
		env = append(env, k+"="+v)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	env = append(env, "HTTP_ADDR=127.0.0.1:"+strconv.Itoa(port))

	logPath := filepath.Join(t.TempDir(), name+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(appBinary)
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	inst := &Instance{
		Name:    name,
		BaseURL: "http://127.0.0.1:" + strconv.Itoa(port),
		LogPath: logPath,
		cmd:     cmd,
		done:    make(chan struct{}),
	}
	go func() {
		_ = cmd.Wait()
		inst.exitCode = cmd.ProcessState.ExitCode()
		logFile.Close()
		close(inst.done)
	}()
	c.mu.Lock()
	c.live = append(c.live, inst)
	c.mu.Unlock()

	t.Cleanup(func() {
		if !inst.Exited() {
			inst.terminate()
			_, _ = inst.wait(stopTimeout)
		}
		logs := inst.Logs()
		if strings.Contains(logs, "WARNING: DATA RACE") {
			t.Errorf("%s: data race detected in the app:\n%s", inst.Name, logs)
		} else if t.Failed() {
			t.Logf("---- %s (%s) logs ----\n%s", inst.Name, inst.BaseURL, tail(logs, 200))
		}
	})
	return inst, nil
}

// Running returns the instances that have not exited, in start order.
func (c *Cluster) Running() []*Instance {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*Instance
	for _, i := range c.live {
		if !i.Exited() {
			out = append(out, i)
		}
	}
	return out
}

// Next picks a running instance round-robin.
func (c *Cluster) Next() *Instance {
	running := c.Running()
	if len(running) == 0 {
		panic("e2e: no running instance")
	}
	return running[c.next.Add(1)%uint64(len(running))]
}

// StopAll stops every running instance (graceful where supported, see
// Instance.Stop) and waits for them to exit.
func (c *Cluster) StopAll(t *testing.T) {
	t.Helper()
	running := c.Running()
	for _, i := range running {
		i.terminate()
	}
	for _, i := range running {
		code, err := i.wait(stopTimeout)
		if err != nil {
			t.Fatalf("%s: %v", i.Name, err)
		}
		if gracefulStop && code != 0 {
			t.Fatalf("%s exited with %d after SIGTERM, want 0:\n%s", i.Name, code, tail(i.Logs(), 50))
		}
	}
}

// Instance is one app process.
type Instance struct {
	Name    string
	BaseURL string
	// LogPath holds the process's stdout and stderr (JSON logs).
	LogPath string

	cmd      *exec.Cmd
	done     chan struct{}
	exitCode int
}

// gracefulStop reports whether Stop sends SIGTERM. Windows cannot deliver a
// signal to another process (os.Process.Signal(os.Interrupt) is not
// implemented there), so Stop kills the process instead; the race run in
// the Linux container exercises the graceful path.
var gracefulStop = runtime.GOOS != "windows"

// Stop terminates the instance (SIGTERM on unix, Kill on Windows) and waits
// for it to exit; on unix it requires exit code 0.
func (i *Instance) Stop(t *testing.T) {
	t.Helper()
	i.terminate()
	code, err := i.wait(stopTimeout)
	if err != nil {
		t.Fatalf("%s: %v", i.Name, err)
	}
	if gracefulStop && code != 0 {
		t.Fatalf("%s exited with %d after SIGTERM, want 0:\n%s", i.Name, code, tail(i.Logs(), 50))
	}
}

// Kill kills the process abruptly (SIGKILL / TerminateProcess) and waits.
func (i *Instance) Kill(t *testing.T) {
	t.Helper()
	_ = i.cmd.Process.Kill()
	if _, err := i.wait(stopTimeout); err != nil {
		t.Fatalf("%s: %v", i.Name, err)
	}
}

// WaitExit waits up to timeout for the process to exit on its own and
// returns its exit code (137 for a crashpoint, see ticket 14).
func (i *Instance) WaitExit(t *testing.T, timeout time.Duration) int {
	t.Helper()
	code, err := i.wait(timeout)
	if err != nil {
		t.Fatalf("%s: %v\n%s", i.Name, err, tail(i.Logs(), 50))
	}
	return code
}

// Exited reports whether the process has exited.
func (i *Instance) Exited() bool {
	select {
	case <-i.done:
		return true
	default:
		return false
	}
}

// ExitCode is the exit code once Exited (-1 when killed by a signal).
func (i *Instance) ExitCode() int {
	<-i.done
	return i.exitCode
}

// Logs returns everything the process wrote so far.
func (i *Instance) Logs() string {
	b, _ := os.ReadFile(i.LogPath)
	return string(b)
}

func (i *Instance) terminate() {
	if gracefulStop {
		_ = i.cmd.Process.Signal(syscall.SIGTERM)
		return
	}
	_ = i.cmd.Process.Kill()
}

func (i *Instance) wait(timeout time.Duration) (int, error) {
	select {
	case <-i.done:
		return i.exitCode, nil
	case <-time.After(timeout):
		_ = i.cmd.Process.Kill()
		<-i.done
		return i.exitCode, fmt.Errorf("did not exit within %s (killed)", timeout)
	}
}

func (i *Instance) waitReady(client *http.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if i.Exited() {
			return fmt.Errorf("%s exited with %d before ready:\n%s", i.Name, i.exitCode, tail(i.Logs(), 50))
		}
		resp, err := client.Get(i.BaseURL + "/health/ready")
		if err == nil {
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Sprintf("%d %s", resp.StatusCode, buf.String())
		} else {
			last = err.Error()
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("%s not ready within %s (last: %s):\n%s", i.Name, timeout, last, tail(i.Logs(), 50))
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
