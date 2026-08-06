package main

import (
	"testing"
	"time"

	"go.guillerg.dev/orbit/internal/state"
)

func TestTaskStatusString(t *testing.T) {
	past := time.Now().Add(-time.Hour)

	tests := []struct {
		name          string
		ts            state.TaskState
		systemdFailed bool
		want          string
	}{
		// Disabled always wins, even over a systemd-level failure.
		{"disabled beats systemd", state.TaskState{Disabled: true, LastRun: past}, true, "disabled"},
		// The recorded failure count is more informative than the systemd flag.
		{"failure count beats systemd", state.TaskState{ConsecutiveFailures: 2, LastRun: past}, true, "failed (2)"},
		// The new branch: systemd reported a failure the state never recorded.
		{"systemd-only failure", state.TaskState{LastRun: past}, true, "failed"},
		// systemd healthy: fall through to state-derived status.
		{"never run", state.TaskState{}, false, "new"},
		{"ok", state.TaskState{LastRun: past}, false, "ok"},
	}

	for _, tc := range tests {
		if got := taskStatusString(tc.ts, tc.systemdFailed); got != tc.want {
			t.Errorf("%s: taskStatusString(%+v, %v) = %q, want %q", tc.name, tc.ts, tc.systemdFailed, got, tc.want)
		}
	}
}

func TestReminderStatusString(t *testing.T) {
	tests := []struct {
		name string
		rs   state.ReminderState
		want string
	}{
		{"disabled wins over pending", state.ReminderState{State: state.StatePending, Disabled: true}, "disabled"},
		{"never fired", state.ReminderState{}, "new"},
		{"pending", state.ReminderState{State: state.StatePending, OverdueCount: 1}, "pending"},
		// A single miss is implied by "pending"; only repeats are worth counting.
		{"repeated overdue", state.ReminderState{State: state.StatePending, OverdueCount: 3}, "pending (3 overdue)"},
		{"snoozed", state.ReminderState{State: state.StateSnoozed, OverdueCount: 2}, "snoozed (2 overdue)"},
		// Overdue counts are stale once acknowledged, so they stay hidden.
		{"acknowledged ignores overdue", state.ReminderState{State: state.StateAcknowledged, OverdueCount: 3}, "acknowledged"},
	}

	for _, tc := range tests {
		if got := reminderStatusString(tc.rs); got != tc.want {
			t.Errorf("%s: reminderStatusString(%+v) = %q, want %q", tc.name, tc.rs, got, tc.want)
		}
	}
}

func TestReminderNextRunPrefersActiveSnooze(t *testing.T) {
	// A half-minute of slack keeps the truncating relative formatter off a boundary.
	future := time.Now().Add(90*time.Minute + 30*time.Second)
	past := time.Now().Add(-time.Hour)
	cfg := state.AppliedReminderConfig{Schedule: "daily"}

	tests := []struct {
		name string
		cfg  state.AppliedReminderConfig
		rs   state.ReminderState
		want string
	}{
		{"disabled", cfg, state.ReminderState{Disabled: true}, cellDisabled},
		{"active snooze", cfg, state.ReminderState{State: state.StateSnoozed, SnoozedUntil: &future}, "in 1h 30m (snoozed)"},
		{"no schedule", state.AppliedReminderConfig{}, state.ReminderState{}, cellManual},
	}

	for _, tc := range tests {
		if got := reminderNextRun(tc.cfg, tc.rs); got != tc.want {
			t.Errorf("%s: reminderNextRun() = %q, want %q", tc.name, got, tc.want)
		}
	}

	// An elapsed snooze must fall through to the schedule rather than showing a
	// next run in the past.
	got := reminderNextRun(state.AppliedReminderConfig{}, state.ReminderState{State: state.StateSnoozed, SnoozedUntil: &past})
	if got != cellManual {
		t.Errorf("expired snooze: reminderNextRun() = %q, want %q", got, cellManual)
	}
}

func TestTaskNextRun(t *testing.T) {
	tests := []struct {
		name string
		cfg  state.AppliedTaskConfig
		ts   state.TaskState
		want string
	}{
		// Disabled wins over the schedule: a disabled task has no next run.
		{"disabled with schedule", state.AppliedTaskConfig{Schedule: "daily"}, state.TaskState{Disabled: true}, cellDisabled},
		{"no schedule", state.AppliedTaskConfig{}, state.TaskState{}, cellManual},
	}

	for _, tc := range tests {
		if got := taskNextRun(tc.cfg, tc.ts); got != tc.want {
			t.Errorf("%s: taskNextRun() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestScheduleCell(t *testing.T) {
	if got := scheduleCell(""); got != cellNone {
		t.Errorf("scheduleCell(\"\") = %q, want %q", got, cellNone)
	}
	if got := scheduleCell("Mon *-*-* 09:00:00"); got != "Mon *-*-* 09:00:00" {
		t.Errorf("scheduleCell() = %q, want the schedule verbatim", got)
	}
}
