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
		attempts      int
		systemdFailed bool
		want          string
	}{
		// Disabled always wins, even over a systemd-level failure.
		{"disabled beats systemd", state.TaskState{Disabled: true, LastRun: past}, 3, true, "disabled"},
		// Settled failures count whole retry cycles, not attempts.
		{"failed cycles beat systemd", state.TaskState{ConsecutiveFailures: 6, FailedCycles: 2, RetryAttempt: 3, LastRun: past}, 3, true, "failed (2)"},
		// A cycle still in flight is retrying, not failed.
		{"retrying", state.TaskState{ConsecutiveFailures: 1, RetryAttempt: 1, LastRun: past}, 3, false, "retrying (1/3)"},
		// Earlier cycles failed and a new one is under way.
		{"failed and retrying", state.TaskState{ConsecutiveFailures: 7, FailedCycles: 2, RetryAttempt: 1, LastRun: past}, 3, false, "failed (2), retrying 1/3"},
		// Without retries an attempt is a cycle, so nothing is ever in flight.
		{"no retries", state.TaskState{ConsecutiveFailures: 1, FailedCycles: 1, RetryAttempt: 1, LastRun: past}, 0, false, "failed (1)"},
		// State written before failed cycles were tracked still reads as failed.
		{"legacy failure", state.TaskState{ConsecutiveFailures: 3, RetryAttempt: 3, LastRun: past}, 3, false, "failed"},
		// systemd reported a failure the state never recorded.
		{"systemd-only failure", state.TaskState{LastRun: past}, 3, true, "failed"},
		// systemd healthy: fall through to state-derived status.
		{"never run", state.TaskState{}, 3, false, "new"},
		{"ok", state.TaskState{LastRun: past}, 3, false, "ok"},
	}

	for _, tc := range tests {
		if got := taskStatusString(tc.ts, tc.attempts, tc.systemdFailed); got != tc.want {
			t.Errorf("%s: taskStatusString(%+v, %d, %v) = %q, want %q", tc.name, tc.ts, tc.attempts, tc.systemdFailed, got, tc.want)
		}
	}
}

func TestReminderStatusString(t *testing.T) {
	ungated := state.AppliedReminderConfig{Schedule: "daily"}

	tests := []struct {
		name string
		cfg  state.AppliedReminderConfig
		rs   state.ReminderState
		want string
	}{
		{"disabled wins over pending", ungated, state.ReminderState{State: state.StatePending, Disabled: true}, "disabled"},
		{"never fired", ungated, state.ReminderState{}, "new"},
		{"pending", ungated, state.ReminderState{State: state.StatePending, OverdueCount: 1}, "pending"},
		// A single miss is implied by "pending"; only repeats are worth counting.
		{"repeated overdue", ungated, state.ReminderState{State: state.StatePending, OverdueCount: 3}, "pending (3 overdue)"},
		{"snoozed", ungated, state.ReminderState{State: state.StateSnoozed, OverdueCount: 2}, "snoozed (2 overdue)"},
		// Overdue counts are stale once acknowledged, so they stay hidden.
		{"acknowledged ignores overdue", ungated, state.ReminderState{State: state.StateAcknowledged, OverdueCount: 3}, "acknowledged"},
	}

	for _, tc := range tests {
		if got := reminderStatusString(tc.cfg, tc.rs); got != tc.want {
			t.Errorf("%s: reminderStatusString(%+v) = %q, want %q", tc.name, tc.rs, got, tc.want)
		}
	}
}

func TestReminderStatusStringCheckNote(t *testing.T) {
	gated := state.AppliedReminderConfig{Schedule: "daily", Check: "test -f /tmp/x"}
	checkedAt := time.Now().Add(-4*time.Hour - 30*time.Second)
	failed, passed := 1, 0

	tests := []struct {
		name string
		cfg  state.AppliedReminderConfig
		rs   state.ReminderState
		want string
	}{
		// The case that made a healthy gated reminder look stalled.
		{
			"failing check explains a stale LAST NOTIFIED",
			gated,
			state.ReminderState{State: state.StateAcknowledged, LastCheckExitCode: &failed, LastCheckAt: checkedAt},
			"acknowledged (checked 4h ago)",
		},
		// A passing check fires in the same run, so its time restates LAST NOTIFIED.
		{
			"passing check adds nothing",
			gated,
			state.ReminderState{State: state.StatePending, LastCheckExitCode: &passed, LastCheckAt: checkedAt},
			"pending",
		},
		// Both notes belong in one parenthetical rather than two.
		{
			"overdue and failing check combine",
			gated,
			state.ReminderState{State: state.StatePending, OverdueCount: 3, LastCheckExitCode: &failed, LastCheckAt: checkedAt},
			"pending (3 overdue, checked 4h ago)",
		},
		// A gated reminder whose check has not run yet looks like any other.
		{
			"check never run",
			gated,
			state.ReminderState{State: state.StateAcknowledged},
			"acknowledged",
		},
		// Stale check state must not leak once the check is removed from config.
		{
			"check removed from config",
			state.AppliedReminderConfig{Schedule: "daily"},
			state.ReminderState{State: state.StateAcknowledged, LastCheckExitCode: &failed, LastCheckAt: checkedAt},
			"acknowledged",
		},
		{
			"disabled suppresses the note",
			gated,
			state.ReminderState{State: state.StateAcknowledged, Disabled: true, LastCheckExitCode: &failed, LastCheckAt: checkedAt},
			"disabled",
		},
	}

	for _, tc := range tests {
		if got := reminderStatusString(tc.cfg, tc.rs); got != tc.want {
			t.Errorf("%s: reminderStatusString() = %q, want %q", tc.name, got, tc.want)
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

func TestOrNone(t *testing.T) {
	if got := orNone(""); got != cellNone {
		t.Errorf("orNone(\"\") = %q, want %q", got, cellNone)
	}
	if got := orNone("Mon *-*-* 09:00:00"); got != "Mon *-*-* 09:00:00" {
		t.Errorf("orNone() = %q, want the value verbatim", got)
	}
}
