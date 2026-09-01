package main

import (
	"os"
	"testing"
)

// colorEnabled is decided from stdout at init, and `go test -fuzz` streams the
// test binary's output straight to the terminal instead of through a pipe. Pin
// it so rendering assertions do not depend on how the suite is invoked.
func TestMain(m *testing.M) {
	colorEnabled = false
	os.Exit(m.Run())
}

// withColor enables colors for the duration of the test.
func withColor(t *testing.T) {
	t.Helper()
	prev := colorEnabled
	colorEnabled = true
	t.Cleanup(func() { colorEnabled = prev })
}
