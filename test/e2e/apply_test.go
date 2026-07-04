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

	// systemctl contract: daemon-reload, then enable --now the timers.
	calls := e.systemctlCalls(t)
	if !hasCall(calls, "daemon-reload") {
		t.Errorf("expected a daemon-reload call, got:\n%s", strings.Join(calls, "\n"))
	}
	if !hasCall(calls, "enable --now") {
		t.Errorf("expected an enable --now call, got:\n%s", strings.Join(calls, "\n"))
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
