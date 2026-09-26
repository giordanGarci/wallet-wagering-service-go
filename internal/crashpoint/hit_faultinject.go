//go:build faultinject

package crashpoint

import (
	"log/slog"
	"os"
	"slices"
	"strings"
)

// Enabled reports whether this build honours CRASH_AT.
const Enabled = true

// ExitCode is the status a crashpoint exits with (the code of a SIGKILLed
// process), so a test tells an injected crash from any other exit.
const ExitCode = 137

// Known lists every crashpoint name; CRASH_AT entries outside it are
// reported at startup and never match.
var Known = []string{OutboxBeforePublish, OutboxAfterPublish, PendingAfterClaim, ConsumerAfterCommit}

// armed holds the crashpoints named by CRASH_AT, read once at startup.
var armed = arm(os.Getenv("CRASH_AT"), slog.New(slog.NewJSONHandler(os.Stderr, nil)))

// Hit exits the process with ExitCode, without running deferred calls or
// shutdown hooks, when CRASH_AT names this crashpoint.
func Hit(name string) {
	if !armed[name] {
		return
	}
	slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("crashpoint hit, exiting", "crashpoint", name, "exitCode", ExitCode)
	os.Exit(ExitCode)
}

// arm parses CRASH_AT (comma-separated names, blanks ignored) and warns
// about unknown names.
func arm(raw string, log *slog.Logger) map[string]bool {
	set := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if !slices.Contains(Known, name) {
			log.Warn("CRASH_AT names an unknown crashpoint; ignored", "crashpoint", name, "known", Known)
			continue
		}
		set[name] = true
	}
	if len(set) > 0 {
		log.Warn("fault injection armed", "crashpoints", raw)
	}
	return set
}
