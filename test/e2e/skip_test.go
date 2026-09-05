//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const skipReminderCfg = "[reminders.standup]\nschedule = \"daily\"\nmessage = \"Standup\"\n"

// TestSkipReminderLifecycle covers skip → suppressed fire → unskip → fire, and
// checks that none of it touches systemd: skip is pure state.
func TestSkipReminderLifecycle(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, skipReminderCfg); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	e.resetCalls(t)

	r := e.run(t, "", "skip", "standup")
	if r.exit != 0 || !contains(r.stdout, "'standup' skipped") {
		t.Fatalf("skip: exit=%d stdout=%q stderr=%s", r.exit, r.stdout, r.stderr)
	}
	if r := e.run(t, "", "reminder", "list", "--all"); !contains(r.stdout, "skipped") {
		t.Errorf("list --all should show skipped:\n%s", r.stdout)
	}

	// The timer fires: the service declines, records why, and leaves no
	// pending reminder behind.
	r = e.run(t, "", "_notify", "standup")
	if r.exit != 0 || !contains(r.stdout, "skipped") {
		t.Fatalf("_notify under skip: exit=%d stdout=%q stderr=%s", r.exit, r.stdout, r.stderr)
	}
	if _, err := os.Stat(e.sentinelPath()); !os.IsNotExist(err) {
		t.Fatalf("skipped fire must not become pending (stat err=%v)", err)
	}
	if calls := e.systemctlCalls(t); hasCall(calls, "daemon-reload") || hasCall(calls, "enable") || hasCall(calls, "start") {
		t.Errorf("skip must not touch units, got systemctl calls:\n%s", joinLines(calls))
	}

	r = e.run(t, "", "unskip", "standup")
	if r.exit != 0 || !contains(r.stdout, "cleared") {
		t.Fatalf("unskip: exit=%d stdout=%q stderr=%s", r.exit, r.stdout, r.stderr)
	}
	if r := e.run(t, "", "unskip", "standup"); r.exit != 0 || !contains(r.stdout, "not skipped") {
		t.Errorf("second unskip: exit=%d stdout=%q", r.exit, r.stdout)
	}
	if r := e.run(t, "", "_notify", "standup"); r.exit != 0 {
		t.Fatalf("_notify after unskip: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if _, err := os.Stat(e.sentinelPath()); err != nil {
		t.Errorf("expected pending reminder after unskip: %v", err)
	}
}

// TestSkipTaskRun checks that a skipped scheduled run neither runs the command
// nor counts as a run, and that the run happens once the skip is cleared.
func TestSkipTaskRun(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	marker := filepath.Join(e.home, "ran")
	cfg := "[tasks.backup]\ncommand = \"touch " + marker + "\"\nschedule = \"daily\"\n"
	if r := e.applyConfig(t, cfg); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	if r := e.run(t, "", "task", "skip", "backup", "--next", "2"); r.exit != 0 || !contains(r.stdout, "2 firings") {
		t.Fatalf("skip: exit=%d stdout=%q stderr=%s", r.exit, r.stdout, r.stderr)
	}

	r := e.run(t, "", "_run", "backup")
	if r.exit != 0 || !contains(r.stdout, "skipped") {
		t.Fatalf("_run under skip: exit=%d stdout=%q stderr=%s", r.exit, r.stdout, r.stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command ran despite skip (stat err=%v)", err)
	}
	st := e.run(t, "", "task", "status", "backup")
	if !contains(st.stdout, "Skipped:") || !contains(st.stdout, "never") {
		t.Errorf("status should show the skip and no run:\n%s", st.stdout)
	}

	// Manual run refuses non-interactively rather than silently clearing.
	if r := e.run(t, "", "run", "backup"); r.exit == 0 || !contains(r.stderr, "unskip") {
		t.Errorf("run on skipped task: exit=%d stderr=%q", r.exit, r.stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("refused run must not execute (stat err=%v)", err)
	}

	if r := e.run(t, "", "unskip", "backup"); r.exit != 0 {
		t.Fatalf("unskip: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "_run", "backup"); r.exit != 0 {
		t.Fatalf("_run after unskip: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("command should have run after unskip: %v", err)
	}
}

// TestSkipRefusals covers the states in which skip must not silently succeed.
func TestSkipRefusals(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	cfg := skipReminderCfg + `
[tasks.manual]
command = "true"

[tasks.oneshot]
command  = "true"
schedule = "2030-01-01 09:00"

[tasks.off]
command  = "true"
schedule = "daily"
`
	if r := e.applyConfig(t, cfg); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "_notify", "standup"); r.exit != 0 {
		t.Fatalf("_notify: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "disable", "off"); r.exit != 0 {
		t.Fatalf("disable: exit=%d stderr=%s", r.exit, r.stderr)
	}

	tests := []struct {
		name    string
		args    []string
		wantSub string
	}{
		{"pending reminder", []string{"skip", "standup"}, "ack or snooze"},
		{"manual task", []string{"skip", "manual"}, "no schedule"},
		{"disabled task", []string{"skip", "off"}, "disabled"},
		{"last firing", []string{"skip", "oneshot"}, "only once more"},
		{"past until", []string{"skip", "oneshot", "--until", "yesterday"}, "in the past"},
		{"unparseable until", []string{"skip", "oneshot", "--until", "next friday"}, "cannot parse"},
		{"wrong kind", []string{"task", "skip", "standup"}, "is a reminder, not a task"},
		{"unknown", []string{"skip", "nope"}, "not found"},
		{"zero", []string{"skip", "oneshot", "--next", "0"}, "at least 1"},
		{"exclusive flags", []string{"skip", "oneshot", "--next", "2", "--until", "tomorrow"}, "none of the others can be"},
	}
	for _, tc := range tests {
		r := e.run(t, "", tc.args...)
		if r.exit == 0 || !contains(r.stderr, tc.wantSub) {
			t.Errorf("%s: exit=%d stderr=%q, want failure containing %q", tc.name, r.exit, r.stderr, tc.wantSub)
		}
	}
}

// TestSkipExpiredWindowClears seeds a skip whose resume point has passed and
// checks the next fire proceeds and drops the stale window.
func TestSkipExpiredWindowClears(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, skipReminderCfg); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	statePath := filepath.Join(e.home, ".local", "share", "orbit", "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	reminders, _ := st["reminders"].(map[string]any)
	if reminders == nil {
		reminders = map[string]any{}
		st["reminders"] = reminders
	}
	reminders["standup"] = map[string]any{"state": "acknowledged", "skip_until": time.Now().Add(-72 * time.Hour).Format(time.RFC3339)}
	data, _ = json.Marshal(st)
	if err := os.WriteFile(statePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if r := e.run(t, "", "reminder", "list", "--all"); contains(r.stdout, "skipped") {
		t.Errorf("expired skip must read as absent:\n%s", r.stdout)
	}
	if r := e.run(t, "", "_notify", "standup"); r.exit != 0 || contains(r.stdout, "skipped") {
		t.Fatalf("_notify with expired skip: exit=%d stdout=%q stderr=%s", r.exit, r.stdout, r.stderr)
	}
	if _, err := os.Stat(e.sentinelPath()); err != nil {
		t.Errorf("expected the fire to go through: %v", err)
	}
	data, _ = os.ReadFile(statePath)
	if contains(string(data), "skip_until") {
		t.Errorf("expired skip should have been cleared from state:\n%s", data)
	}
}

// TestDisableClearsSkip: disable is the stronger verb, so re-enabling later must
// not resurrect a stale skip window.
func TestDisableClearsSkip(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, skipReminderCfg); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "skip", "standup", "--until", "+30d"); r.exit != 0 {
		t.Fatalf("skip: exit=%d stderr=%s", r.exit, r.stderr)
	}
	for _, verb := range []string{"disable", "enable"} {
		if r := e.run(t, "", verb, "standup"); r.exit != 0 {
			t.Fatalf("%s: exit=%d stderr=%s", verb, r.exit, r.stderr)
		}
	}
	if r := e.run(t, "", "reminder", "list", "--all"); contains(r.stdout, "skipped") {
		t.Errorf("skip survived disable/enable:\n%s", r.stdout)
	}
}

// TestRootSkipResolvesKind checks the root shortcut dispatches by name and the
// namespaced forms reject the other kind.
func TestRootSkipResolvesKind(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	cfg := skipReminderCfg + "\n[tasks.backup]\ncommand = \"true\"\nschedule = \"daily\"\n"
	if r := e.applyConfig(t, cfg); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "skip", "backup"); r.exit != 0 || !contains(r.stdout, "Task 'backup' skipped") {
		t.Errorf("root skip task: exit=%d stdout=%q stderr=%s", r.exit, r.stdout, r.stderr)
	}
	if r := e.run(t, "", "skip", "standup"); r.exit != 0 || !contains(r.stdout, "Reminder 'standup' skipped") {
		t.Errorf("root skip reminder: exit=%d stdout=%q stderr=%s", r.exit, r.stdout, r.stderr)
	}
	if r := e.run(t, "", "reminder", "skip", "backup"); r.exit == 0 || !contains(r.stderr, "is a task, not a reminder") {
		t.Errorf("reminder skip on task: exit=%d stderr=%q", r.exit, r.stderr)
	}
	if r := e.run(t, "", "reminder", "unskip", "backup"); r.exit == 0 {
		t.Errorf("reminder unskip on task should be rejected, stdout=%q", r.stdout)
	}
	r := e.run(t, "", "list")
	if !contains(r.stdout, "skipped") {
		t.Errorf("list should show both skipped:\n%s", r.stdout)
	}
}

// TestApplyClearsSkipOnScheduleChange: a skip is "not the next N of this
// schedule". Once the schedule changes it no longer means anything, so apply
// drops it and says so.
func TestApplyClearsSkipOnScheduleChange(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	weekly := "[tasks.report]\ncommand = \"true\"\nschedule = \"Mon 09:00\"\n"
	if r := e.applyConfig(t, weekly); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "skip", "report"); r.exit != 0 {
		t.Fatalf("skip: exit=%d stderr=%s", r.exit, r.stderr)
	}

	// Unrelated change keeps the skip.
	r := e.applyConfig(t, "[tasks.report]\ncommand = \"echo changed\"\nschedule = \"Mon 09:00\"\n")
	if r.exit != 0 || contains(r.stdout, "skip on") {
		t.Fatalf("command-only change: exit=%d stdout=%q", r.exit, r.stdout)
	}
	if r := e.run(t, "", "task", "list"); !contains(r.stdout, "skipped") {
		t.Errorf("skip should survive a command change:\n%s", r.stdout)
	}

	r = e.applyConfig(t, "[tasks.report]\ncommand = \"echo changed\"\nschedule = \"daily\"\n")
	if r.exit != 0 || !contains(r.stdout, "skip on 'report' cleared") {
		t.Fatalf("schedule change: exit=%d stdout=%q stderr=%s", r.exit, r.stdout, r.stderr)
	}
	if r := e.run(t, "", "task", "list"); contains(r.stdout, "skipped") {
		t.Errorf("skip should not survive a schedule change:\n%s", r.stdout)
	}
}
