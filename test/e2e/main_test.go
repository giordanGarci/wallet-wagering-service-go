//go:build e2e

// Package e2e_test runs the compiled service as three independent processes
// (own port, pool and memory each) against the shared test infrastructure:
// PostgreSQL, Keycloak and MiniStack (see README, "Testes").
//
//	go test -tags=e2e -timeout 10m ./test/e2e/...
//	KEYCLOAK_PORT=8082 scripts/test-race.sh -tags=e2e -timeout 10m ./test/e2e/...
//
// TestMain builds cmd/app once (with -race when the test binary has it, with
// -tags faultinject under the faultinject tag); every test then provisions
// its own database and queues (testsupport.NewInfra) and starts a Cluster.
package e2e_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// appBinary is the path of the compiled cmd/app, set by TestMain.
var appBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "wallet-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: temp dir:", err)
		os.Exit(1)
	}
	code := func() int {
		defer os.RemoveAll(dir)
		appBinary, err = buildApp(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e:", err)
			return 1
		}
		return m.Run()
	}()
	os.Exit(code)
}

// buildApp compiles ./cmd/app from the module root into dir.
func buildApp(dir string) (string, error) {
	out := filepath.Join(dir, "app")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	args := []string{"build", "-o", out}
	if raceEnabled {
		args = append(args, "-race")
	}
	if faultinjectEnabled {
		args = append(args, "-tags", "faultinject")
	}
	args = append(args, "./cmd/app")
	cmd := exec.Command("go", args...)
	cmd.Dir = filepath.Join("..", "..") // the test runs in test/e2e
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go %v: %v\n%s", args, err, b)
	}
	return out, nil
}
