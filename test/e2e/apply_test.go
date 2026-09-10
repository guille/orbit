//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApply runs `orbit apply` from a config file, asserting the full pipeline
func TestApply(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	e.writeConfig(t, `orbit_bin = "`+orbitBin+`"

[tasks.backup]
schedule = "daily"
command = "echo hi"

[reminders.standup]
schedule = "Mon *-*-* 09:00:00"
message = "Standup"
`)

	r := e.run(t, "", "apply")
	if r.exit != 0 {
		t.Fatalf("apply exit=%d\nstdout: %s\nstderr: %s", r.exit, r.stdout, r.stderr)
	}

	// Units installed on disk. Reaching here with exit 0 means they also passed
	// orbit's real `systemd-analyze verify`.
	for _, name := range []string{
		"orbit-task-backup.service",
		"orbit-task-backup.timer",
		"orbit-reminder-standup.service",
		"orbit-reminder-standup.timer",
	} {
		if _, err := os.Stat(filepath.Join(e.unitDir(), name)); err != nil {
			t.Errorf("expected unit %s installed: %v", name, err)
		}
	}

	// systemctl contract: enable --now the timers, and nothing else. enable
	// reloads the manager itself. See docs/daemon-reloads.md.
	calls := e.systemctlCalls(t)
	if !hasCall(calls, "enable --now") {
		t.Errorf("expected an enable --now call, got:\n%s", strings.Join(calls, "\n"))
	}
	if hasCall(calls, "daemon-reload") {
		t.Errorf("enable reloads already; expected no explicit daemon-reload, got:\n%s", strings.Join(calls, "\n"))
	}
}

// TestApplyIdempotent verifies a second apply with no config change is a no-op.
func TestApplyIdempotent(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	e.writeConfig(t, `orbit_bin = "`+orbitBin+`"

[tasks.backup]
schedule = "daily"
command = "echo hi"
`)

	if r := e.run(t, "", "apply"); r.exit != 0 {
		t.Fatalf("first apply failed: exit=%d stderr=%s", r.exit, r.stderr)
	}

	r := e.run(t, "", "apply")
	if r.exit != 0 {
		t.Fatalf("second apply failed: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if !strings.Contains(r.stdout, "up to date") {
		t.Errorf("expected 'up to date' on unchanged re-apply, got: %s", r.stdout)
	}
}

// TestApplyScheduleList applies a task with several OnCalendar expressions and
// checks they all land in the installed timer.
func TestApplyScheduleList(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	e.writeConfig(t, `orbit_bin = "`+orbitBin+`"

[tasks.standup]
schedule = ["Mon..Fri 09:00", "Mon..Fri 13:30"]
command = "echo hi"
`)
	if r := e.run(t, "", "apply"); r.exit != 0 {
		t.Fatalf("apply exit=%d\nstdout: %s\nstderr: %s", r.exit, r.stdout, r.stderr)
	}

	unit, err := os.ReadFile(filepath.Join(e.unitDir(), "orbit-task-standup.timer"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "OnCalendar=Mon..Fri 09:00\nOnCalendar=Mon..Fri 13:30\n") {
		t.Errorf("timer lacks both OnCalendar lines:\n%s", unit)
	}

	r := e.run(t, "", "next")
	if r.exit != 0 || !strings.Contains(r.stdout, "Mon..Fri 09:00 | Mon..Fri 13:30") || strings.Contains(r.stdout, "could not resolve") {
		t.Errorf("next: exit=%d\n%s", r.exit, r.stdout)
	}

	// Dropping an expression is an update that rewrites the timer.
	e.writeConfig(t, `orbit_bin = "`+orbitBin+`"

[tasks.standup]
schedule = "Mon..Fri 09:00"
command = "echo hi"
`)
	r = e.run(t, "", "apply")
	if r.exit != 0 || !strings.Contains(r.stdout, "Mon..Fri 09:00 | Mon..Fri 13:30") {
		t.Fatalf("second apply: exit=%d\nstdout: %s\nstderr: %s", r.exit, r.stdout, r.stderr)
	}
	unit, _ = os.ReadFile(filepath.Join(e.unitDir(), "orbit-task-standup.timer"))
	if strings.Contains(string(unit), "13:30") {
		t.Errorf("timer still has the removed expression:\n%s", unit)
	}
}
