package main

import (
	"fmt"
	"time"

	"go.guillerg.dev/orbit/internal/reminder"
	"go.guillerg.dev/orbit/internal/state"
)

// Placeholder cells shared by every listing.
const (
	cellNone     = "-"          // no value configured; not applicable
	cellNever    = "never"      // has not happened yet
	cellManual   = "(manual)"   // runs only on demand
	cellDisabled = "(disabled)" // suppressed by 'orbit disable'
)

// Column headers. Listings use a subset in this order.
const (
	colName      = "NAME"
	colType      = "TYPE"
	colSchedule  = "SCHEDULE"
	colLastRun   = "LAST RUN"
	colLastFired = "LAST FIRED"
	colNextRun   = "NEXT RUN"
	colStatus    = "STATUS"
)

// scheduleCell renders a SCHEDULE cell.
func scheduleCell(schedule string) string {
	if schedule == "" {
		return cellNone
	}
	return schedule
}

// taskNextRun renders a task's NEXT RUN cell.
func taskNextRun(taskConfig state.AppliedTaskConfig, ts state.TaskState) string {
	if ts.Disabled {
		return dim(cellDisabled)
	}
	if taskConfig.Schedule == "" {
		return dim(cellManual)
	}
	return resolveNextRun(taskConfig.Schedule)
}

// reminderNextRun renders a reminder's NEXT RUN cell. An active snooze takes
// precedence over the configured schedule.
func reminderNextRun(reminderConfig state.AppliedReminderConfig, rs state.ReminderState) string {
	if rs.Disabled {
		return dim(cellDisabled)
	}
	if rs.State == state.StateSnoozed && rs.SnoozedUntil != nil && rs.SnoozedUntil.After(time.Now()) {
		return formatTime(*rs.SnoozedUntil) + " (snoozed)"
	}
	if reminderConfig.Schedule == "" {
		return dim(cellManual)
	}
	return resolveNextRun(reminderConfig.Schedule)
}

// taskStatusString returns a colored display string for a task's current status.
// systemdFailed reports whether the task's last run failed at the systemd level,
// covering failures the persisted state never recorded.
func taskStatusString(ts state.TaskState, systemdFailed bool) string {
	switch {
	case ts.Disabled:
		return dim("disabled")
	case ts.ConsecutiveFailures > 0:
		return red(fmt.Sprintf("failed (%d)", ts.ConsecutiveFailures))
	case systemdFailed:
		return red("failed")
	case ts.LastRun.IsZero():
		return dim("new")
	default:
		return green("ok")
	}
}

// reminderStatusString returns a display string for a reminder's current state,
// folding in a repeated-overdue count when one is pending.
func reminderStatusString(rs state.ReminderState) string {
	if rs.Disabled {
		return dim("disabled")
	}
	display := colorizeReminderState(rs.State)
	if rs.OverdueCount > 1 && reminder.IsActionable(rs) {
		display += fmt.Sprintf(" (%d overdue)", rs.OverdueCount)
	}
	return display
}

// colorizeReminderState applies color to a reminder state string for display.
func colorizeReminderState(s state.ReminderStatus) string {
	switch s {
	case state.StatePending, state.StateSnoozed:
		return yellow(s.String())
	case state.StateAcknowledged:
		return green(s.String())
	default:
		return dim(s.String())
	}
}
