package main

import (
	"os"
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
	}
	fields := []string{"command", "schedule", "on_missed", "retry.attempts", "retry.delay"}
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
