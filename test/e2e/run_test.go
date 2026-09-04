//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunExitCodes checks the `orbit _run` exit-code contract: 10 when the
// command failed after all retries, 1 when orbit itself could not run the task.
func TestRunExitCodes(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	cfg := `
[tasks.ok]
command = "true"

[tasks.broken]
command        = "exit 3"
retry.attempts = 2
retry.delay    = "0s"
`
	if r := e.applyConfig(t, cfg); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	if r := e.run(t, "", "_run", "ok"); r.exit != 0 {
		t.Errorf("succeeding task: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "_run", "broken"); r.exit != 10 {
		t.Errorf("failing task should exit 10, got %d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "_run", "missing"); r.exit != 1 {
		t.Errorf("unknown task is orbit's failure and should exit 1, got %d stderr=%s", r.exit, r.stderr)
	}
}

// TestRunIfFailedHook applies a failing task with an if_failed hook and checks
// the hook fires once per exhausted retry cycle, only after the `after`
// threshold, with the documented environment.
func TestRunIfFailedHook(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	out := filepath.Join(e.home, "hook.out")
	cfg := `
[tasks.broken]
command           = "exit 3"
retry.attempts    = 2
retry.delay       = "0s"
if_failed.command = "env | grep ^ORBIT_ >> ` + out + `"
if_failed.after   = 2
`
	if r := e.applyConfig(t, cfg); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	if r := e.run(t, "", "_run", "broken"); r.exit != 10 {
		t.Fatalf("first cycle: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("hook fired after one failed cycle with after=2 (stat err: %v)", err)
	}

	if r := e.run(t, "", "_run", "broken"); r.exit != 10 {
		t.Fatalf("second cycle: exit=%d stderr=%s", r.exit, r.stderr)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("hook did not fire on the second failed cycle: %v", err)
	}
	for _, want := range []string{
		"ORBIT_TASK=broken\n",
		"ORBIT_EXIT_CODE=3\n",
		"ORBIT_ATTEMPTS=2\n",
		"ORBIT_CONSECUTIVE_FAILURES=4\n",
		"ORBIT_FAILED_CYCLES=2\n",
		"ORBIT_DURATION_MS=",
	} {
		if !contains(string(got), want) {
			t.Errorf("hook environment missing %q:\n%s", want, got)
		}
	}
}
