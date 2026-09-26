//go:build !faultinject

package crashpoint

// Enabled reports whether this build honours CRASH_AT. It is false outside
// fault-injection builds, where Hit compiles to a no-op and nothing reads
// CRASH_AT.
const Enabled = false

// Hit does nothing outside fault-injection builds.
func Hit(name string) {}
