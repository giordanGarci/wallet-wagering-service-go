//go:build faultinject

package crashpoint

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestArmParsesKnownNamesAndWarnsAboutUnknownOnes(t *testing.T) {
	var logs bytes.Buffer
	got := arm(" outbox.after_publish, ,nope,pending.after_claim", slog.New(slog.NewJSONHandler(&logs, nil)))
	if len(got) != 2 || !got[OutboxAfterPublish] || !got[PendingAfterClaim] {
		t.Fatalf("armed = %v, want outbox.after_publish and pending.after_claim", got)
	}
	if !strings.Contains(logs.String(), `"crashpoint":"nope"`) {
		t.Errorf("no warning about the unknown name:\n%s", logs.String())
	}
	if got := arm("", slog.New(slog.NewJSONHandler(&logs, nil))); len(got) != 0 {
		t.Errorf("empty CRASH_AT armed %v", got)
	}
}

// The test binary re-runs itself with CRASH_AT set; Hit must exit with 137
// only at the named crashpoint.
func TestHitExitsWith137AtTheNamedCrashpoint(t *testing.T) {
	if os.Getenv("CRASHPOINT_CHILD") == "1" {
		Hit(OutboxBeforePublish) // not armed: returns
		Hit(ConsumerAfterCommit)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHitExitsWith137AtTheNamedCrashpoint$")
	cmd.Env = append(os.Environ(), "CRASHPOINT_CHILD=1", "CRASH_AT="+ConsumerAfterCommit)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != ExitCode {
		t.Fatalf("child err = %v, want exit %d:\n%s", err, ExitCode, out)
	}
	if !strings.Contains(string(out), `"crashpoint":"consumer.after_commit"`) {
		t.Errorf("no crash log line:\n%s", out)
	}
}
