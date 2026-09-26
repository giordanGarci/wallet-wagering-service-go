//go:build e2e && race

package e2e_test

// raceEnabled is true when the test binary runs with -race; the app binary
// is then built with -race too, so data races inside the instances fail the
// suite (see Instance cleanup).
const raceEnabled = true
