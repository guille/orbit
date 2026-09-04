package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"go.guillerg.dev/orbit/internal/config"
	"go.guillerg.dev/orbit/internal/state"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{0, "0ms"},
		{-1, "0ms"},
		{1, "1ms"},
		{500, "500ms"},
		{999, "999ms"},
		{1000, "1s"},
		{1300, "1s 300ms"},
		{60_000, "1m"},
		{70_000, "1m 10s"},
		{300_000, "5m"},
		{310_000, "5m 10s"},
		{3_600_000, "1h"},
		{4_800_000, "1h 20m"},
		{86_400_000, "24h"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.ms)
		if got != tt.want {
			t.Errorf("formatDuration(%d) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}

func TestDiffConfig_NewTasks(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"backup": {Command: "rsync", Schedule: "daily", OnMissed: "run_once"},
		},
	}

	cs := diffConfig(cfg, nil)
	if cs.nCreate != 1 || cs.nUpdate != 0 || cs.nRemove != 0 {
		t.Fatalf("expected 1 create, got %d create, %d update, %d remove", cs.nCreate, cs.nUpdate, cs.nRemove)
	}
	if cs.changes[0].name != "backup" || cs.changes[0].kind != kindTask {
		t.Errorf("expected task backup, got %s %s", cs.changes[0].kind, cs.changes[0].name)
	}
}

func TestDiffConfig_UpdatedTask(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"backup": {Command: "rsync -av", Schedule: "weekly", OnMissed: "run_once"},
		},
	}
	applied := &state.AppliedConfig{
		Tasks: map[string]state.AppliedTaskConfig{
			"backup": {Command: "rsync", Schedule: "daily", OnMissed: "run_once"},
		},
		Reminders: make(map[string]state.AppliedReminderConfig),
	}

	cs := diffConfig(cfg, applied)
	if cs.nCreate != 0 || cs.nUpdate != 1 || cs.nRemove != 0 {
		t.Fatalf("expected 1 update, got %d create, %d update, %d remove", cs.nCreate, cs.nUpdate, cs.nRemove)
	}
}

func TestDiffConfig_RemovedTask(t *testing.T) {
	cfg := &config.Config{
		Tasks:     make(map[string]config.TaskConfig),
		Reminders: make(map[string]config.ReminderConfig),
	}
	applied := &state.AppliedConfig{
		Tasks: map[string]state.AppliedTaskConfig{
			"old": {Command: "echo", Schedule: "daily"},
		},
		Reminders: make(map[string]state.AppliedReminderConfig),
	}

	cs := diffConfig(cfg, applied)
	if cs.nRemove != 1 {
		t.Fatalf("expected 1 remove, got %d", cs.nRemove)
	}
	if cs.changes[0].action != "remove" || cs.changes[0].name != "old" {
		t.Errorf("expected remove old, got %s %s", cs.changes[0].action, cs.changes[0].name)
	}
}

func TestDiffConfig_UnchangedTask(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"backup": {Command: "rsync", Schedule: "daily", OnMissed: "run_once", Retry: config.RetryConfig{Attempts: new(3), Delay: "5m"}},
		},
	}
	applied := &state.AppliedConfig{
		Tasks: map[string]state.AppliedTaskConfig{
			"backup": {Command: "rsync", Schedule: "daily", OnMissed: "run_once", Retry: state.AppliedRetryConfig{Attempts: 3, Delay: "5m"}},
		},
		Reminders: make(map[string]state.AppliedReminderConfig),
	}

	cs := diffConfig(cfg, applied)
	if len(cs.changes) != 0 {
		t.Fatalf("expected 0 changes, got %d", len(cs.changes))
	}
	if cs.nUnchanged != 1 {
		t.Errorf("expected 1 unchanged, got %d", cs.nUnchanged)
	}
}

func TestDiffConfig_Reminders(t *testing.T) {
	cfg := &config.Config{
		Tasks: make(map[string]config.TaskConfig),
		Reminders: map[string]config.ReminderConfig{
			"review":  {Schedule: "weekly", Message: "Do review", Snooze: "2h"},
			"standup": {Schedule: "daily", Message: "New standup"},
		},
	}
	applied := &state.AppliedConfig{
		Tasks: make(map[string]state.AppliedTaskConfig),
		Reminders: map[string]state.AppliedReminderConfig{
			"review": {Schedule: "weekly", Message: "Old message", Snooze: "2h"},
			"old":    {Schedule: "daily", Message: "Removed"},
		},
	}

	cs := diffConfig(cfg, applied)
	if cs.nCreate != 1 || cs.nUpdate != 1 || cs.nRemove != 1 {
		t.Fatalf("expected 1 create, 1 update, 1 remove; got %d/%d/%d", cs.nCreate, cs.nUpdate, cs.nRemove)
	}
}

func TestDiffConfig_SortOrder(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"new-task": {Command: "echo", Schedule: "daily", OnMissed: "skip"},
		},
		Reminders: map[string]config.ReminderConfig{
			"updated": {Schedule: "daily", Message: "New msg", Snooze: "1h"},
		},
	}
	applied := &state.AppliedConfig{
		Tasks: map[string]state.AppliedTaskConfig{
			"removed": {Command: "old", Schedule: "daily"},
		},
		Reminders: map[string]state.AppliedReminderConfig{
			"updated": {Schedule: "daily", Message: "Old msg", Snooze: "1h"},
		},
	}

	cs := diffConfig(cfg, applied)
	if len(cs.changes) != 3 {
		t.Fatalf("expected 3 changes, got %d", len(cs.changes))
	}
	// Creates first, then updates, then removes
	if cs.changes[0].action != "create" {
		t.Errorf("first change should be create, got %s", cs.changes[0].action)
	}
	if cs.changes[1].action != "update" {
		t.Errorf("second change should be update, got %s", cs.changes[1].action)
	}
	if cs.changes[2].action != "remove" {
		t.Errorf("third change should be remove, got %s", cs.changes[2].action)
	}
}

func TestTaskChanged(t *testing.T) {
	base := state.AppliedTaskConfig{Command: "echo", Schedule: "daily", OnMissed: "run_once", Retry: state.AppliedRetryConfig{Attempts: 3, Delay: "5m"}}
	same := config.TaskConfig{Command: "echo", Schedule: "daily", OnMissed: "run_once", Retry: config.RetryConfig{Attempts: new(3), Delay: "5m"}}

	if taskChanged(base, same) {
		t.Error("identical configs should not be changed")
	}

	tests := []config.TaskConfig{
		{Command: "different", Schedule: "daily", OnMissed: "run_once", Retry: config.RetryConfig{Attempts: new(3), Delay: "5m"}},
		{Command: "echo", Schedule: "weekly", OnMissed: "run_once", Retry: config.RetryConfig{Attempts: new(3), Delay: "5m"}},
		{Command: "echo", Schedule: "daily", OnMissed: "skip", Retry: config.RetryConfig{Attempts: new(3), Delay: "5m"}},
		{Command: "echo", Schedule: "daily", OnMissed: "run_once", Retry: config.RetryConfig{Attempts: new(5), Delay: "5m"}},
		{Command: "echo", Schedule: "daily", OnMissed: "run_once", Retry: config.RetryConfig{Attempts: new(3), Delay: "10m"}},
		{Command: "echo", Schedule: "daily", OnMissed: "run_once", Retry: config.RetryConfig{Attempts: new(3), Delay: "5m"}, IfFailed: config.HookConfig{Command: "notify", After: new(1)}},
	}
	fields := []string{"command", "schedule", "on_missed", "retry.attempts", "retry.delay", "if_failed.command"}
	for i, tc := range tests {
		if !taskChanged(base, tc) {
			t.Errorf("changing %s should be detected", fields[i])
		}
	}
}

func TestPrintConfigChanges_Output(t *testing.T) {
	cs := configChangeSet{
		changes: []configChange{
			{name: "new", kind: kindTask, action: actionCreate, newTask: &config.TaskConfig{Command: "echo", Schedule: "daily", OnMissed: "skip"}},
			{name: "upd", kind: kindTask, action: actionUpdate,
				oldTask: &state.AppliedTaskConfig{Command: "old", Schedule: "daily"},
				newTask: &config.TaskConfig{Command: "new", Schedule: "weekly"}},
			{name: "del", kind: kindReminder, action: actionRemove},
		},
		nCreate: 1, nUpdate: 1, nRemove: 1,
	}

	output := captureStdout(t, func() {
		printConfigChanges(cs)
	})

	if !strings.Contains(output, "new") {
		t.Error("expected output to contain created task name")
	}
	if !strings.Contains(output, "upd") {
		t.Error("expected output to contain updated task name")
	}
	if !strings.Contains(output, "del") {
		t.Error("expected output to contain removed reminder name")
	}
}

func TestDiffField_Output(t *testing.T) {
	// Same values: no output
	output := captureStdout(t, func() {
		diffField("test", "same", "same")
	})
	if output != "" {
		t.Errorf("expected no output for same values, got %q", output)
	}

	// Different values: should show diff
	output = captureStdout(t, func() {
		diffField("test", "old", "new")
	})
	if !strings.Contains(output, "old") || !strings.Contains(output, "new") {
		t.Errorf("expected diff output showing old and new, got %q", output)
	}
}

// captureStdout redirects stdout and returns what was printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	//nolint:errcheck
	w.Close()
	os.Stdout = old
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	//nolint:errcheck
	r.Close()
	return string(buf[:n])
}

func TestDiffConfig_UnscheduledTask(t *testing.T) {
	// New unscheduled task
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"deploy": {Command: "./deploy.sh"},
		},
	}

	cs := diffConfig(cfg, nil)
	if cs.nCreate != 1 {
		t.Fatalf("expected 1 create, got %d", cs.nCreate)
	}
	if cs.changes[0].newTask.Schedule != "" {
		t.Errorf("expected empty schedule, got %q", cs.changes[0].newTask.Schedule)
	}
}

func TestDiffConfig_ScheduleAdded(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"deploy": {Command: "./deploy.sh", Schedule: "daily", OnMissed: "run_once"},
		},
	}
	applied := &state.AppliedConfig{
		Tasks: map[string]state.AppliedTaskConfig{
			"deploy": {Command: "./deploy.sh", Schedule: ""},
		},
		Reminders: make(map[string]state.AppliedReminderConfig),
	}

	cs := diffConfig(cfg, applied)
	if cs.nUpdate != 1 {
		t.Fatalf("expected 1 update (schedule added), got %d updates", cs.nUpdate)
	}
}

func TestDiffConfig_ScheduleRemoved(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"deploy": {Command: "./deploy.sh"},
		},
	}
	applied := &state.AppliedConfig{
		Tasks: map[string]state.AppliedTaskConfig{
			"deploy": {Command: "./deploy.sh", Schedule: "daily", OnMissed: "run_once"},
		},
		Reminders: make(map[string]state.AppliedReminderConfig),
	}

	cs := diffConfig(cfg, applied)
	if cs.nUpdate != 1 {
		t.Fatalf("expected 1 update (schedule removed), got %d updates", cs.nUpdate)
	}
	// Verify the change has oldTask with schedule and newTask without
	c := cs.changes[0]
	if c.oldTask.Schedule != "daily" {
		t.Errorf("expected old schedule 'daily', got %q", c.oldTask.Schedule)
	}
	if c.newTask.Schedule != "" {
		t.Errorf("expected new schedule empty, got %q", c.newTask.Schedule)
	}
}

func TestDiffConfig_UnscheduledUnchanged(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"deploy": {Command: "./deploy.sh", Retry: config.RetryConfig{Attempts: new(3), Delay: "5m"}},
		},
	}
	applied := &state.AppliedConfig{
		Tasks: map[string]state.AppliedTaskConfig{
			"deploy": {Command: "./deploy.sh", Retry: state.AppliedRetryConfig{Attempts: 3, Delay: "5m"}},
		},
		Reminders: make(map[string]state.AppliedReminderConfig),
	}

	cs := diffConfig(cfg, applied)
	if len(cs.changes) != 0 {
		t.Fatalf("expected 0 changes for identical unscheduled task, got %d", len(cs.changes))
	}
	if cs.nUnchanged != 1 {
		t.Errorf("expected 1 unchanged, got %d", cs.nUnchanged)
	}
}

func TestDiffConfig_ReminderCheckChanged(t *testing.T) {
	cfg := &config.Config{
		Tasks: make(map[string]config.TaskConfig),
		Reminders: map[string]config.ReminderConfig{
			"pacnew": {Schedule: "daily", Message: "Check pacnew", Check: "locate .pacnew", Snooze: "2h"},
		},
	}
	applied := &state.AppliedConfig{
		Tasks: make(map[string]state.AppliedTaskConfig),
		Reminders: map[string]state.AppliedReminderConfig{
			"pacnew": {Schedule: "daily", Message: "Check pacnew", Check: "", Snooze: "2h"},
		},
	}

	cs := diffConfig(cfg, applied)
	if cs.nUpdate != 1 {
		t.Fatalf("expected 1 update (check added), got %d", cs.nUpdate)
	}
}

func TestDiffConfig_ReminderCheckUnchanged(t *testing.T) {
	cfg := &config.Config{
		Tasks: make(map[string]config.TaskConfig),
		Reminders: map[string]config.ReminderConfig{
			"pacnew": {Schedule: "daily", Message: "Check pacnew", Check: "locate .pacnew", Snooze: "2h"},
		},
	}
	applied := &state.AppliedConfig{
		Tasks: make(map[string]state.AppliedTaskConfig),
		Reminders: map[string]state.AppliedReminderConfig{
			"pacnew": {Schedule: "daily", Message: "Check pacnew", Check: "locate .pacnew", Snooze: "2h"},
		},
	}

	cs := diffConfig(cfg, applied)
	if len(cs.changes) != 0 {
		t.Fatalf("expected 0 changes, got %d", len(cs.changes))
	}
}

func TestDiffConfig_OrbitBinChangeMarksAllForUpdate(t *testing.T) {
	cfg := &config.Config{
		OrbitBin: "/new/orbit/path",
		Tasks: map[string]config.TaskConfig{
			"backup": {Command: "rsync", Schedule: "daily", OnMissed: "run_once"},
		},
		Reminders: map[string]config.ReminderConfig{
			"review": {Schedule: "weekly", Message: "Do review"},
		},
	}
	applied := &state.AppliedConfig{
		OrbitBin: "/old/orbit/path",
		Tasks: map[string]state.AppliedTaskConfig{
			"backup": {Command: "rsync", Schedule: "daily", OnMissed: "run_once"},
		},
		Reminders: map[string]state.AppliedReminderConfig{
			"review": {Schedule: "weekly", Message: "Do review"},
		},
	}

	cs := diffConfig(cfg, applied)
	if cs.nCreate != 0 {
		t.Errorf("expected 0 creates, got %d", cs.nCreate)
	}
	if cs.nUpdate != 2 {
		t.Fatalf("expected 2 updates (task + reminder), got %d", cs.nUpdate)
	}
	if cs.nRemove != 0 {
		t.Errorf("expected 0 removes, got %d", cs.nRemove)
	}
	if cs.nUnchanged != 0 {
		t.Errorf("expected 0 unchanged (all forced to update by OrbitBin change), got %d", cs.nUnchanged)
	}
	for _, c := range cs.changes {
		if c.action != actionUpdate {
			t.Errorf("expected all changes to be updates, got %s for %s", c.action, c.name)
		}
	}
}

func TestDiffConfig_AllRemoved(t *testing.T) {
	// Empty config against existing applied → all removed
	cfg := &config.Config{
		Tasks:     map[string]config.TaskConfig{},
		Reminders: map[string]config.ReminderConfig{},
	}
	applied := &state.AppliedConfig{
		Tasks: map[string]state.AppliedTaskConfig{
			"backup": {Command: "rsync", Schedule: "daily"},
			"deploy": {Command: "./deploy.sh"},
		},
		Reminders: map[string]state.AppliedReminderConfig{
			"weekly": {Schedule: "weekly", Message: "hi"},
		},
	}

	cs := diffConfig(cfg, applied)
	if cs.nRemove != 3 {
		t.Fatalf("expected 3 removes, got %d", cs.nRemove)
	}
	if cs.nCreate != 0 || cs.nUpdate != 0 {
		t.Fatalf("expected no creates/updates, got create=%d update=%d", cs.nCreate, cs.nUpdate)
	}
}

func TestUnitsToRemove(t *testing.T) {
	scheduledOld := state.AppliedTaskConfig{Schedule: "daily"}
	unscheduledNew := config.TaskConfig{Schedule: ""}
	keptNew := config.TaskConfig{Schedule: "daily"}

	cs := configChangeSet{changes: []configChange{
		{name: "backup", kind: kindTask, action: actionRemove},
		{name: "standup", kind: kindReminder, action: actionRemove},
		// A task that lost its schedule: service stays, timer must go.
		{name: "cron", kind: kindTask, action: actionUpdate, oldTask: &scheduledOld, newTask: &unscheduledNew},
		// A task that kept its schedule: nothing removed.
		{name: "keep", kind: kindTask, action: actionUpdate, oldTask: &scheduledOld, newTask: &keptNew},
	}}

	units, removed := unitsToRemove(cs)

	wantUnits := []string{
		"orbit-task-backup.service", "orbit-task-backup.timer",
		"orbit-reminder-standup.service", "orbit-reminder-standup.timer", "orbit-snooze-standup.timer",
		"orbit-task-cron.timer",
	}
	var gotUnits []string
	for _, u := range units {
		gotUnits = append(gotUnits, u.Name)
	}
	if !slices.Equal(gotUnits, wantUnits) {
		t.Errorf("units = %v, want %v", gotUnits, wantUnits)
	}

	// Only the two actionRemove entries get their state deleted; the lost-schedule
	// task is an update, not a removal.
	if len(removed) != 2 {
		t.Fatalf("expected 2 removed entries, got %d: %v", len(removed), removed)
	}
	if removed[0].name != "backup" || removed[0].kind != kindTask {
		t.Errorf("removed[0] = %s %s, want task backup", removed[0].kind, removed[0].name)
	}
	if removed[1].name != "standup" || removed[1].kind != kindReminder {
		t.Errorf("removed[1] = %s %s, want reminder standup", removed[1].kind, removed[1].name)
	}
}

// writtenUnitNames returns the sorted names of the units unitsToWrite emits.
// unitsToWrite iterates config maps, so only the set is deterministic.
func writtenUnitNames(t *testing.T, cfg *config.Config, cs configChangeSet, force bool) []string {
	t.Helper()
	units, err := unitsToWrite(cfg, cs, force)
	if err != nil {
		t.Fatalf("unitsToWrite: %v", err)
	}
	names := make([]string, 0, len(units))
	for _, u := range units {
		names = append(names, u.Name)
	}
	slices.Sort(names)
	return names
}

// twoTaskTwoReminderCfg is a config with two of each kind, so tests can assert
// that untouched entries are left alone.
func twoTaskTwoReminderCfg() *config.Config {
	return &config.Config{
		Tasks: map[string]config.TaskConfig{
			"backup": {Command: "rsync", Schedule: "daily", OnMissed: "run_once"},
			"deploy": {Command: "./deploy.sh", Schedule: "weekly", OnMissed: "run_once"},
		},
		Reminders: map[string]config.ReminderConfig{
			"review":  {Schedule: "weekly", Message: "Do review"},
			"standup": {Schedule: "daily", Message: "Standup"},
		},
	}
}

func TestUnitsToWrite_OnlyChangedEntries(t *testing.T) {
	cfg := twoTaskTwoReminderCfg()
	cs := configChangeSet{changes: []configChange{
		{name: "backup", kind: kindTask, action: actionUpdate},
	}}

	got := writtenUnitNames(t, cfg, cs, false)
	want := []string{"orbit-task-backup.service", "orbit-task-backup.timer"}
	if !slices.Equal(got, want) {
		t.Errorf("unitsToWrite = %v, want only the changed task's pair %v", got, want)
	}
}

func TestUnitsToWrite_ForceWritesEverything(t *testing.T) {
	cfg := twoTaskTwoReminderCfg()
	// An empty changeset with force still regenerates every entry.
	got := writtenUnitNames(t, cfg, configChangeSet{}, true)
	want := []string{
		"orbit-reminder-review.service", "orbit-reminder-review.timer",
		"orbit-reminder-standup.service", "orbit-reminder-standup.timer",
		"orbit-task-backup.service", "orbit-task-backup.timer",
		"orbit-task-deploy.service", "orbit-task-deploy.timer",
	}
	if !slices.Equal(got, want) {
		t.Errorf("unitsToWrite(force) = %v, want all 8 units %v", got, want)
	}
}

func TestUnitsToWrite_NoChangesWritesNothing(t *testing.T) {
	cfg := twoTaskTwoReminderCfg()
	if got := writtenUnitNames(t, cfg, configChangeSet{}, false); len(got) != 0 {
		t.Errorf("expected no units without changes or force, got %v", got)
	}
}

func TestUnitsToWrite_RemovalsWriteNothing(t *testing.T) {
	// A removed entry is gone from cfg, so it cannot be generated; a removal must
	// not drag any other entry's units in either.
	cfg := twoTaskTwoReminderCfg()
	cs := configChangeSet{changes: []configChange{
		{name: "gone", kind: kindTask, action: actionRemove},
		{name: "alsogone", kind: kindReminder, action: actionRemove},
	}}

	if got := writtenUnitNames(t, cfg, cs, false); len(got) != 0 {
		t.Errorf("expected no units for a removal-only changeset, got %v", got)
	}
}

func TestUnitsToWrite_OrbitBinChangeWritesEverything(t *testing.T) {
	// diffConfig marks every entry as updated when orbit_bin changes, so the
	// narrowing must not defeat that path.
	cfg := twoTaskTwoReminderCfg()
	cfg.OrbitBin = "/new/orbit"
	applied := &state.AppliedConfig{
		OrbitBin: "/old/orbit",
		Tasks: map[string]state.AppliedTaskConfig{
			"backup": {Command: "rsync", Schedule: "daily", OnMissed: "run_once"},
			"deploy": {Command: "./deploy.sh", Schedule: "weekly", OnMissed: "run_once"},
		},
		Reminders: map[string]state.AppliedReminderConfig{
			"review":  {Schedule: "weekly", Message: "Do review"},
			"standup": {Schedule: "daily", Message: "Standup"},
		},
	}

	cs := diffConfig(cfg, applied)
	got := writtenUnitNames(t, cfg, cs, false)
	if len(got) != 8 {
		t.Errorf("orbit_bin change should regenerate all 8 units, got %d: %v", len(got), got)
	}
	for _, n := range got {
		if !strings.Contains(n, "backup") && !strings.Contains(n, "deploy") &&
			!strings.Contains(n, "review") && !strings.Contains(n, "standup") {
			t.Errorf("unexpected unit %q", n)
		}
	}
}

func TestUnitsToWrite_ScheduleGainedAndLost(t *testing.T) {
	// Gaining a schedule adds a timer; losing one drops it (unitsToRemove deletes
	// the stale timer, so unitsToWrite must simply not emit it).
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"gains": {Command: "echo", Schedule: "daily", OnMissed: "run_once"},
			"loses": {Command: "echo"},
		},
	}
	cs := configChangeSet{changes: []configChange{
		{name: "gains", kind: kindTask, action: actionUpdate},
		{name: "loses", kind: kindTask, action: actionUpdate},
	}}

	got := writtenUnitNames(t, cfg, cs, false)
	want := []string{
		"orbit-task-gains.service", "orbit-task-gains.timer",
		"orbit-task-loses.service", // no timer: unscheduled
	}
	if !slices.Equal(got, want) {
		t.Errorf("unitsToWrite = %v, want %v", got, want)
	}
}

func TestUnitsToWrite_SameNameTaskAndReminder(t *testing.T) {
	// A name may be both a task and a reminder, so the changed-entry lookup is
	// keyed per kind. Changing only the task must not regenerate the reminder.
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"sync": {Command: "rsync", Schedule: "daily", OnMissed: "run_once"},
		},
		Reminders: map[string]config.ReminderConfig{
			"sync": {Schedule: "weekly", Message: "Sync it"},
		},
	}
	cs := configChangeSet{changes: []configChange{
		{name: "sync", kind: kindTask, action: actionUpdate},
	}}

	got := writtenUnitNames(t, cfg, cs, false)
	want := []string{"orbit-task-sync.service", "orbit-task-sync.timer"}
	if !slices.Equal(got, want) {
		t.Errorf("unitsToWrite = %v, want only the task's units %v", got, want)
	}
}

func TestChangedEntries_PartitionsByKind(t *testing.T) {
	cs := configChangeSet{changes: []configChange{
		{name: "a", kind: kindTask, action: actionCreate},
		{name: "b", kind: kindTask, action: actionUpdate},
		{name: "c", kind: kindTask, action: actionRemove},
		{name: "d", kind: kindReminder, action: actionCreate},
		{name: "e", kind: kindReminder, action: actionRemove},
		// same name, both kinds: each side tracked independently
		{name: "shared", kind: kindTask, action: actionUpdate},
	}}

	tasks, reminders := changedEntries(cs)

	for _, name := range []string{"a", "b", "shared"} {
		if !tasks[name] {
			t.Errorf("expected task %q to be marked changed", name)
		}
	}
	if tasks["c"] {
		t.Error("removed task should not be marked for regeneration")
	}
	if !reminders["d"] {
		t.Error("expected reminder d to be marked changed")
	}
	if reminders["e"] {
		t.Error("removed reminder should not be marked for regeneration")
	}
	if reminders["shared"] {
		t.Error("a changed task must not mark a same-named reminder")
	}
	if len(tasks) != 3 || len(reminders) != 1 {
		t.Errorf("got %d tasks / %d reminders, want 3 / 1", len(tasks), len(reminders))
	}
}

type fakeStateReader struct {
	tasks     map[string]state.TaskState
	reminders map[string]state.ReminderState
}

func (f fakeStateReader) GetTaskState(name string) state.TaskState {
	return f.tasks[name]
}

func (f fakeStateReader) GetReminderState(name string) state.ReminderState {
	return f.reminders[name]
}

// disabledCfg has one disabled and one enabled entry of each kind, all
// scheduled, so every entry has a timer that could need disabling.
func disabledCfg() (*config.Config, fakeStateReader) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"enabled-task":  {Schedule: "daily"},
			"disabled-task": {Schedule: "daily"},
		},
		Reminders: map[string]config.ReminderConfig{
			"enabled-rem":  {Schedule: "daily"},
			"disabled-rem": {Schedule: "daily"},
		},
	}
	sr := fakeStateReader{
		tasks: map[string]state.TaskState{
			"disabled-task": {Disabled: true},
		},
		reminders: map[string]state.ReminderState{
			"disabled-rem": {Disabled: true},
		},
	}
	return cfg, sr
}

func TestTimersToDisable_OnlyChangedEntries(t *testing.T) {
	cfg, sr := disabledCfg()

	// The disabled task changed, so installing re-enabled its timer.
	cs := configChangeSet{changes: []configChange{
		{name: "disabled-task", kind: kindTask, action: actionUpdate},
	}}
	got := timersToDisable(cfg, sr, cs, false)
	want := []string{"orbit-task-disabled-task.timer"}
	if !slices.Equal(got, want) {
		t.Errorf("timersToDisable = %v, want %v", got, want)
	}
}

func TestTimersToDisable_UntouchedDisabledEntrySkipped(t *testing.T) {
	cfg, sr := disabledCfg()

	// Only an enabled entry changed: nothing was re-enabled, so nothing needs
	// disabling. Disabling here would cost a daemon-reload for nothing.
	cs := configChangeSet{changes: []configChange{
		{name: "enabled-task", kind: kindTask, action: actionUpdate},
	}}
	if got := timersToDisable(cfg, sr, cs, false); len(got) != 0 {
		t.Errorf("expected no timers to disable, got %v", got)
	}
}

func TestTimersToDisable_ForceCoversAllDisabled(t *testing.T) {
	cfg, sr := disabledCfg()

	// force reinstalls (and so re-enables) everything, including entries with no
	// config change, so every disabled timer must be put back.
	got := timersToDisable(cfg, sr, configChangeSet{}, true)
	want := []string{"orbit-reminder-disabled-rem.timer", "orbit-task-disabled-task.timer"}
	if !slices.Equal(got, want) {
		t.Errorf("timersToDisable(force) = %v, want %v", got, want)
	}
}

func TestTimersToDisable_SkipsManualTask(t *testing.T) {
	// An unscheduled task never gets a timer generated, so naming one here would
	// hand systemctl a unit that does not exist.
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"manual":    {},
			"scheduled": {Schedule: "daily"},
		},
	}
	sr := fakeStateReader{tasks: map[string]state.TaskState{
		"manual":    {Disabled: true},
		"scheduled": {Disabled: true},
	}}

	got := timersToDisable(cfg, sr, configChangeSet{}, true)
	want := []string{"orbit-task-scheduled.timer"}
	if !slices.Equal(got, want) {
		t.Errorf("timersToDisable = %v, want %v (no timer for the manual task)", got, want)
	}

	// It is still a disabled entry for reporting purposes.
	if n := disabledEntryCount(cfg, sr); n != 2 {
		t.Errorf("disabledEntryCount = %d, want 2 (manual tasks count too)", n)
	}
}

func TestTimersToDisable_NoneDisabled(t *testing.T) {
	cfg := &config.Config{
		Tasks:     map[string]config.TaskConfig{"a": {}},
		Reminders: map[string]config.ReminderConfig{"b": {}},
	}
	cs := configChangeSet{changes: []configChange{
		{name: "a", kind: kindTask, action: actionUpdate},
		{name: "b", kind: kindReminder, action: actionUpdate},
	}}
	if got := timersToDisable(cfg, fakeStateReader{}, cs, false); len(got) != 0 {
		t.Errorf("expected no disabled timers, got %v", got)
	}
}

func TestDisabledEntryCount(t *testing.T) {
	cfg, sr := disabledCfg()

	// The summary counts every disabled entry, regardless of what apply touched.
	if got := disabledEntryCount(cfg, sr); got != 2 {
		t.Errorf("disabledEntryCount = %d, want 2", got)
	}
	if got := disabledEntryCount(cfg, fakeStateReader{}); got != 0 {
		t.Errorf("disabledEntryCount with none disabled = %d, want 0", got)
	}
}

func TestTaskChanged_IfFailedAfter(t *testing.T) {
	old := state.AppliedTaskConfig{Command: "echo", Retry: state.AppliedRetryConfig{Attempts: 3, Delay: "5m"}, IfFailed: state.AppliedHookConfig{Command: "notify", After: 1}}
	same := config.TaskConfig{Command: "echo", Retry: config.RetryConfig{Attempts: new(3), Delay: "5m"}, IfFailed: config.HookConfig{Command: "notify", After: new(1)}}
	if taskChanged(old, same) {
		t.Error("identical hook config should not be changed")
	}

	bumped := same
	bumped.IfFailed.After = new(2)
	if !taskChanged(old, bumped) {
		t.Error("changing if_failed.after should be detected")
	}
}

func TestToAppliedConfig_IfFailed(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"hooked": {Command: "exit 1", IfFailed: config.HookConfig{Command: "notify", After: new(2)}},
			"plain":  {Command: "exit 0"},
		},
	}
	ac := toAppliedConfig(cfg)

	if got := ac.Tasks["hooked"].IfFailed; got != (state.AppliedHookConfig{Command: "notify", After: 2}) {
		t.Errorf("unexpected applied hook: %+v", got)
	}
	if got := ac.Tasks["plain"].IfFailed; got != (state.AppliedHookConfig{}) {
		t.Errorf("task without a hook should have a zero applied hook, got %+v", got)
	}
}
