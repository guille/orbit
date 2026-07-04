//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// TestApplyUpdatesChangedEntry re-applies after a config change and expects the
// changed task to be reported as an update.
func TestApplyUpdatesChangedEntry(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, "[tasks.backup]\nschedule = \"daily\"\ncommand = \"echo hi\"\n"); r.exit != 0 {
		t.Fatalf("initial apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	r := e.applyConfig(t, "[tasks.backup]\nschedule = \"weekly\"\ncommand = \"echo hi\"\n")
	if r.exit != 0 {
		t.Fatalf("update apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if !contains(r.stdout, "update") || !contains(r.stdout, "backup") {
		t.Errorf("expected an update for 'backup', got:\n%s", r.stdout)
	}

	// The regenerated timer reflects the new schedule.
	timer := filepath.Join(e.unitDir(), "orbit-task-backup.timer")
	data, err := os.ReadFile(timer)
	if err != nil {
		t.Fatalf("reading timer: %v", err)
	}
	if !contains(string(data), "OnCalendar=weekly") {
		t.Errorf("expected OnCalendar=weekly in regenerated timer, got:\n%s", data)
	}
}

// TestApplyRemovesOrphans drops an entry from config and expects its units to be
// stopped, disabled, and deleted.
func TestApplyRemovesOrphans(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	both := "[tasks.backup]\nschedule = \"daily\"\ncommand = \"echo hi\"\n\n" +
		"[reminders.standup]\nschedule = \"Mon *-*-* 09:00:00\"\nmessage = \"Standup\"\n"
	if r := e.applyConfig(t, both); r.exit != 0 {
		t.Fatalf("initial apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	r := e.applyConfig(t, "[tasks.backup]\nschedule = \"daily\"\ncommand = \"echo hi\"\n")
	if r.exit != 0 {
		t.Fatalf("prune apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	// Reminder units gone, task units kept.
	for _, gone := range []string{"orbit-reminder-standup.service", "orbit-reminder-standup.timer"} {
		if _, err := os.Stat(filepath.Join(e.unitDir(), gone)); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, stat err=%v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(e.unitDir(), "orbit-task-backup.timer")); err != nil {
		t.Errorf("expected backup timer kept: %v", err)
	}

	// Contract: the orphan's timer was stopped and disabled.
	calls := e.systemctlCalls(t)
	if !hasCall(calls, "stop") || !hasCall(calls, "orbit-reminder-standup.timer") {
		t.Errorf("expected stop/disable of orphaned reminder timer, calls:\n%s", joinLines(calls))
	}
}
