package main

import (
	"fmt"
	"strings"
	"time"

	"go.guillerg.dev/orbit/internal/reminder"
	"go.guillerg.dev/orbit/internal/state"
	"go.guillerg.dev/orbit/internal/systemd"
)

// Placeholder cells shared by every listing.
const (
	cellNone     = "-"          // no value configured; not applicable
	cellNever    = "never"      // has not happened yet
	cellManual   = "(manual)"   // runs only on demand
	cellDisabled = "(disabled)" // suppressed by 'orbit disable'
	cellSkipped  = "(skipped)"  // suppressed by 'orbit skip'; prefixed by the resume time
)

// Column headers. Listings use a subset in this order.
const (
	colName         = "NAME"
	colType         = "TYPE"
	colSchedule     = "SCHEDULE"
	colLastRun      = "LAST RUN"
	colLastNotified = "LAST NOTIFIED"
	colNextRun      = "NEXT RUN"
	colStatus       = "STATUS"
)

// orNone renders an optional config value, marking an unset one.
func orNone(value string) string {
	if value == "" {
		return cellNone
	}
	return value
}

// taskNextRun renders a task's NEXT RUN cell.
func taskNextRun(taskConfig state.AppliedTaskConfig, ts state.TaskState) string {
	if ts.Disabled {
		return dim(cellDisabled)
	}
	if resume, ok := skipResume(taskConfig.Schedule, ts.SkipUntil); ok {
		return skippedCell(resume)
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
	if resume, ok := skipResume(reminderConfig.Schedule, rs.SkipUntil); ok {
		return skippedCell(resume)
	}
	if rs.State == state.StateSnoozed && rs.SnoozedUntil != nil && rs.SnoozedUntil.After(time.Now()) {
		return formatTime(*rs.SnoozedUntil) + " (snoozed)"
	}
	if reminderConfig.Schedule == "" {
		return dim(cellManual)
	}
	return resolveNextRun(reminderConfig.Schedule)
}

// taskStatusString returns a colored display string for a task's current
// status. An active skip reads as skipped even over a recorded failure: it is
// what happens next that the listing answers.
func taskStatusString(taskConfig state.AppliedTaskConfig, ts state.TaskState, systemdFailed bool) string {
	if !ts.Disabled && isSkipped(taskConfig.Schedule, ts.SkipUntil) {
		return dim("skipped")
	}
	return taskRunStatus(ts, taskConfig.Retry.Attempts, systemdFailed)
}

// taskRunStatus describes a task's last run. attempts is the configured retry
// count, which tells an in-flight retry cycle from an exhausted one.
// systemdFailed reports whether the task's last run failed at the systemd
// level, covering failures the persisted state never recorded.
func taskRunStatus(ts state.TaskState, attempts int, systemdFailed bool) string {
	retrying := ts.RetryAttempt > 0 && ts.RetryAttempt < max(attempts, 1)
	switch {
	case ts.Disabled:
		return dim("disabled")
	case ts.FailedCycles > 0 && retrying:
		return red(fmt.Sprintf("failed (%d)", ts.FailedCycles)) + yellow(fmt.Sprintf(", retrying %d/%d", ts.RetryAttempt, attempts))
	case ts.FailedCycles > 0:
		return red(fmt.Sprintf("failed (%d)", ts.FailedCycles))
	case retrying:
		return yellow(fmt.Sprintf("retrying (%d/%d)", ts.RetryAttempt, attempts))
	case ts.ConsecutiveFailures > 0:
		// State written before failed cycles were tracked.
		return red("failed")
	case systemdFailed:
		return red("failed")
	case ts.LastRun.IsZero():
		return dim("new")
	default:
		return green("ok")
	}
}

// reminderStatusString returns a display string for a reminder's current state.
func reminderStatusString(reminderConfig state.AppliedReminderConfig, rs state.ReminderState) string {
	if rs.Disabled {
		return dim("disabled")
	}
	if isSkipped(reminderConfig.Schedule, rs.SkipUntil) {
		return dim("skipped")
	}

	var notes []string
	if rs.OverdueCount > 1 && reminder.IsActionable(rs) {
		notes = append(notes, fmt.Sprintf("%d overdue", rs.OverdueCount))
	}
	if note := lastCheckedString(reminderConfig, rs); note != "" {
		notes = append(notes, note)
	}

	display := colorizeReminderState(rs.State)
	if len(notes) > 0 {
		display += fmt.Sprintf(" (%s)", strings.Join(notes, ", "))
	}
	return display
}

// skippedCell renders the NEXT RUN cell of a skipped entry. A zero resume
// means the schedule could not be resolved, so no time is shown.
func skippedCell(resume time.Time) string {
	if resume.IsZero() {
		return dim(cellSkipped)
	}
	return formatTime(resume) + " " + cellSkipped
}

// isSkipped reports whether an entry is under an active skip window.
func isSkipped(schedule string, skipUntil *time.Time) bool {
	_, ok := skipResume(schedule, skipUntil)
	return ok
}

// lastCheckedString reports how recently a gating check ran, but only while that check
// is failing.
func lastCheckedString(reminderConfig state.AppliedReminderConfig, rs state.ReminderState) string {
	if reminderConfig.Check == "" || rs.LastCheckExitCode == nil || *rs.LastCheckExitCode == 0 {
		return ""
	}
	if rs.LastCheckAt.IsZero() {
		return ""
	}
	return "checked " + formatTime(rs.LastCheckAt)
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

// systemdResult describes a failed unit's outcome, e.g. "exit-code 203" or "signal".
func systemdResult(st systemd.UnitStatus) string {
	if st.Result == "exit-code" {
		return fmt.Sprintf("exit-code %d", st.ExitStatus)
	}
	return st.Result
}
