//go:build e2e

package e2e

import "testing"

const taskAndReminder = "[tasks.backup]\nschedule = \"daily\"\ncommand = \"echo hi\"\n\n" +
	"[reminders.standup]\nschedule = \"Mon *-*-* 09:00:00\"\nmessage = \"Standup\"\n"

// TestListShowsEntries checks that `orbit list` renders both configured entries.
func TestListShowsEntries(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, taskAndReminder); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	r := e.run(t, "", "list")
	if r.exit != 0 {
		t.Fatalf("list: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if !contains(r.stdout, "backup") || !contains(r.stdout, "standup") {
		t.Errorf("expected both entries in list output, got:\n%s", r.stdout)
	}
}

// TestNextShowsUpcoming checks that `orbit next` lists entries with a next-run
// column resolved via real systemd-analyze.
func TestNextShowsUpcoming(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, taskAndReminder); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	r := e.run(t, "", "next")
	if r.exit != 0 {
		t.Fatalf("next: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if !contains(r.stdout, "backup") || !contains(r.stdout, "standup") {
		t.Errorf("expected both entries in next output, got:\n%s", r.stdout)
	}
}
