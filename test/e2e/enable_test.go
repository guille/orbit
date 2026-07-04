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
