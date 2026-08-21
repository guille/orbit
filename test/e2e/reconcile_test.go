//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// threeEntryConfig has two tasks and a reminder, so scoping assertions have
// untouched entries to check against.
const threeEntryConfig = "[tasks.backup]\nschedule = \"daily\"\ncommand = \"echo hi\"\n\n" +
	"[tasks.deploy]\nschedule = \"weekly\"\ncommand = \"echo deploy\"\n\n" +
	"[reminders.standup]\nschedule = \"Mon *-*-* 09:00:00\"\nmessage = \"Standup\"\n"

// unitFileIDs snapshots the identity of every installed unit file. apply
// installs by renaming over the old file, so a replaced unit gets a new inode.
func unitFileIDs(t *testing.T, dir string) map[string]os.FileInfo {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading unit dir: %v", err)
	}
	ids := make(map[string]os.FileInfo, len(entries))
	for _, ent := range entries {
		if !strings.HasPrefix(ent.Name(), "orbit-") {
			continue
		}
		fi, err := os.Stat(filepath.Join(dir, ent.Name()))
		if err != nil {
			t.Fatalf("stat %s: %v", ent.Name(), err)
		}
		ids[ent.Name()] = fi
	}
	return ids
}

// rewritten names the unit files replaced between two snapshots.
func rewritten(before, after map[string]os.FileInfo) []string {
	var names []string
	for name, a := range after {
		if b, ok := before[name]; !ok || !os.SameFile(a, b) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// enableCall returns the recorded `systemctl enable` invocation.
func enableCall(t *testing.T, calls []string) string {
	t.Helper()
	for _, c := range calls {
		if strings.Contains(c, " enable ") {
			return c
		}
	}
	return ""
}

// TestApplyScopesWorkToChangedEntries locks in that apply only reinstalls the
// entries whose config changed. Reinstalling an unchanged unit is not a no-op:
// it costs a systemd-analyze verify and an enable round trip, both of which grow
// with the size of the config.
func TestApplyScopesWorkToChangedEntries(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, threeEntryConfig); r.exit != 0 {
		t.Fatalf("initial apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	before := unitFileIDs(t, e.unitDir())
	if len(before) != 6 {
		t.Fatalf("expected 6 unit files after initial apply, got %d", len(before))
	}
	e.resetCalls(t)

	// Change one field of one entry.
	changed := strings.Replace(threeEntryConfig, `command = "echo hi"`, `command = "echo changed"`, 1)
	if r := e.applyConfig(t, changed); r.exit != 0 {
		t.Fatalf("re-apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	got := rewritten(before, unitFileIDs(t, e.unitDir()))
	want := []string{"orbit-task-backup.service", "orbit-task-backup.timer"}
	if !slices.Equal(got, want) {
		t.Errorf("rewritten units = %v, want only the changed entry's pair %v", got, want)
	}

	// Only the changed entry's timer is re-enabled.
	enable := enableCall(t, e.systemctlCalls(t))
	if enable == "" {
		t.Fatalf("expected an enable call, got:\n%s", joinLines(e.systemctlCalls(t)))
	}
	if !contains(enable, "orbit-task-backup.timer") {
		t.Errorf("expected changed timer in enable call, got: %s", enable)
	}
	for _, untouched := range []string{"orbit-task-deploy.timer", "orbit-reminder-standup.timer"} {
		if contains(enable, untouched) {
			t.Errorf("unchanged %s should not be re-enabled, got: %s", untouched, enable)
		}
	}
}

// TestApplyForceRewritesEverything guards the escape hatch: --force must still
// regenerate every unit, since that is what repairs drift on disk.
func TestApplyForceRewritesEverything(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, threeEntryConfig); r.exit != 0 {
		t.Fatalf("initial apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	before := unitFileIDs(t, e.unitDir())
	e.resetCalls(t)

	// Same config, so the changeset is empty and only --force does any work.
	if r := e.applyConfig(t, threeEntryConfig, "--force"); r.exit != 0 {
		t.Fatalf("force apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	got := rewritten(before, unitFileIDs(t, e.unitDir()))
	if len(got) != len(before) {
		t.Errorf("--force rewrote %d of %d units (%v), want all", len(got), len(before), got)
	}

	enable := enableCall(t, e.systemctlCalls(t))
	for _, timer := range []string{"orbit-task-backup.timer", "orbit-task-deploy.timer", "orbit-reminder-standup.timer"} {
		if !contains(enable, timer) {
			t.Errorf("--force should re-enable %s, got: %s", timer, enable)
		}
	}
}

// TestApplyForceRepairsDeletedUnit is the drift case that scoping deliberately
// leaves to --force: a unit deleted behind orbit's back is restored.
func TestApplyForceRepairsDeletedUnit(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, threeEntryConfig); r.exit != 0 {
		t.Fatalf("initial apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	victim := filepath.Join(e.unitDir(), "orbit-task-deploy.timer")
	if err := os.Remove(victim); err != nil {
		t.Fatal(err)
	}

	if r := e.applyConfig(t, threeEntryConfig, "--force"); r.exit != 0 {
		t.Fatalf("force apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("--force should restore the deleted unit, stat err=%v", err)
	}
}

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
