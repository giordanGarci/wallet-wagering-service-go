//go:build e2e && faultinject

package e2e_test

// faultinjectEnabled is true under -tags=e2e,faultinject; the app binary is
// then built with -tags faultinject, so CRASH_AT is honoured (ticket 14).
const faultinjectEnabled = true
