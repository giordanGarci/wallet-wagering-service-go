//go:build !faultinject

package crashpoint_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/crashpoint"
)

func TestNormalBuildHasNoCrashHook(t *testing.T) {
	if crashpoint.Enabled {
		t.Fatal("crashpoint.Enabled = true without the faultinject tag")
	}
	crashpoint.Hit(crashpoint.OutboxBeforePublish) // no-op: the test goes on
}

// The hook's state (crashpoint.armed, which reads CRASH_AT) is linked into
// cmd/app only with -tags faultinject. The faultinject build is the control
// that proves the symbol lookup works.
func TestAppBinaryContainsHookOnlyWithFaultinjectTag(t *testing.T) {
	if testing.Short() {
		t.Skip("builds cmd/app twice")
	}
	const symbol = "internal/crashpoint.armed"
	for _, tc := range []struct {
		tags string
		want bool
	}{{"", false}, {"faultinject", true}} {
		out := filepath.Join(t.TempDir(), "app")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		args := []string{"build", "-o", out}
		if tc.tags != "" {
			args = append(args, "-tags", tc.tags)
		}
		build := exec.Command("go", append(args, "./cmd/app")...)
		build.Dir = filepath.Join("..", "..")
		if b, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, b)
		}
		syms, err := exec.Command("go", "tool", "nm", out).CombinedOutput()
		if err != nil {
			t.Fatalf("go tool nm: %v\n%s", err, syms)
		}
		if got := strings.Contains(string(syms), symbol); got != tc.want {
			t.Errorf("tags %q: binary contains %s = %v, want %v", tc.tags, symbol, got, tc.want)
		}
	}
}
