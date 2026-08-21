//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDisableEnableTask exercises disabling and re-enabling a task, asserting
// the systemctl contract and that unit files are retained (disable ≠ remove).
func TestDisableEnableTask(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, "[tasks.backup]\nschedule = \"daily\"\ncommand = \"echo hi\"\n"); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	if r := e.run(t, "", "disable", "backup"); r.exit != 0 {
		t.Fatalf("disable: exit=%d stderr=%s", r.exit, r.stderr)
	}

	// Timer stopped and disabled, but the unit file remains on disk.
	calls := e.systemctlCalls(t)
	if !hasCall(calls, "disable") || !hasCall(calls, "orbit-task-backup.timer") {
		t.Errorf("expected disable of the task timer, calls:\n%s", joinLines(calls))
	}
	if _, err := os.Stat(filepath.Join(e.unitDir(), "orbit-task-backup.timer")); err != nil {
		t.Errorf("expected timer file retained after disable: %v", err)
	}

	if r := e.run(t, "", "enable", "backup"); r.exit != 0 {
		t.Fatalf("enable: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if !hasCall(e.systemctlCalls(t), "enable") {
		t.Errorf("expected an enable call after re-enabling")
	}
}

// TestDisableManualTaskNamesNoTimer checks that disabling an unscheduled task
// does not hand systemctl a timer unit: none is ever generated for one. The
// state flag must still flip, which is the whole of what disabling a manual task
// does — nothing stops it being run on demand.
func TestDisableManualTaskNamesNoTimer(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	cfg := "[tasks.manual]\ncommand = \"echo hi\"\n\n" +
		"[tasks.scheduled]\nschedule = \"daily\"\ncommand = \"echo hi\"\n"
	if r := e.applyConfig(t, cfg); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(e.unitDir(), "orbit-task-manual.timer")); !os.IsNotExist(err) {
		t.Fatalf("a manual task should have no timer, stat err=%v", err)
	}

	e.resetCalls(t)
	if r := e.run(t, "", "disable", "manual"); r.exit != 0 {
		t.Fatalf("disable: exit=%d stderr=%s", r.exit, r.stderr)
	}

	if calls := e.systemctlCalls(t); hasCall(calls, "orbit-task-manual.timer") {
		t.Errorf("disable named a timer that was never generated, calls:\n%s", joinLines(calls))
	}
	if r := e.run(t, "", "list"); !contains(r.stdout, "disabled") {
		t.Errorf("expected the manual task to read as disabled, got:\n%s", r.stdout)
	}
}

// TestApplyLeavesUntouchedDisabledEntryAlone checks that apply does not disable
// timers it never installed. `disable` implies a daemon-reload, so re-asserting
// the state of an entry apply did not touch costs a reload for nothing.
func TestApplyLeavesUntouchedDisabledEntryAlone(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, threeEntryConfig); r.exit != 0 {
		t.Fatalf("initial apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "disable", "deploy"); r.exit != 0 {
		t.Fatalf("disable: exit=%d stderr=%s", r.exit, r.stderr)
	}

	e.resetCalls(t)
	// Change only backup; deploy stays disabled and untouched.
	changed := replaceOnce(threeEntryConfig, `command = "echo hi"`, `command = "echo changed"`)
	if r := e.applyConfig(t, changed); r.exit != 0 {
		t.Fatalf("re-apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	calls := e.systemctlCalls(t)
	if hasCall(calls, "disable") {
		t.Errorf("apply should not disable an untouched entry, calls:\n%s", joinLines(calls))
	}
	// The summary still reports it as disabled.
	if r := e.run(t, "", "apply"); !contains(r.stdout, "1 entries disabled") && !contains(r.stdout, "up to date") {
		t.Errorf("expected the disabled entry still accounted for, got:\n%s", r.stdout)
	}
}

// TestApplyForceReassertsDisabled is the other half: --force reinstalls (and so
// re-enables) every timer, so disabled entries must be put back.
func TestApplyForceReassertsDisabled(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, threeEntryConfig); r.exit != 0 {
		t.Fatalf("initial apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "disable", "deploy"); r.exit != 0 {
		t.Fatalf("disable: exit=%d stderr=%s", r.exit, r.stderr)
	}

	e.resetCalls(t)
	if r := e.applyConfig(t, threeEntryConfig, "--force"); r.exit != 0 {
		t.Fatalf("force apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	calls := e.systemctlCalls(t)
	if !hasCall(calls, "disable") {
		t.Fatalf("--force should re-disable the disabled entry, calls:\n%s", joinLines(calls))
	}
	for _, c := range calls {
		if contains(c, "disable") && !contains(c, "orbit-task-deploy.timer") {
			t.Errorf("expected deploy's timer in the disable call, got: %s", c)
		}
	}
}
