// Package reminder encapsulates reminder state transitions.
package reminder

import (
	"errors"
	"os/exec"
	"time"

	"go.guillerg.dev/orbit/internal/state"
)

// StateTracker defines the interface for state operations needed by Handler.
// The fire path writes through UpdateReminderState: a slow check command must
// not let Fire overwrite what the user changed on the entry meanwhile.
type StateTracker interface {
	GetReminderState(name string) state.ReminderState
	SetReminderState(name string, rs state.ReminderState)
	UpdateReminderState(name string, fn func(*state.ReminderState)) error
	Save() error
}

// CheckRunner runs a reminder's check command and reports its exit code.
type CheckRunner interface {
	Run(command string) (exitCode int, err error)
}

// shellCheckRunner runs checks via sh -c, discarding output.
type shellCheckRunner struct{}

func (shellCheckRunner) Run(command string) (int, error) {
	err := exec.Command("sh", "-c", command).Run()
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode(), nil
	}
	return 1, err
}

// Handler manages reminder state transitions.
type Handler struct {
	State StateTracker
	Check CheckRunner
}

// NewHandler creates a new reminder handler with the default shell check runner.
func NewHandler(s StateTracker) *Handler {
	return &Handler{State: s, Check: shellCheckRunner{}}
}

// IsActionable reports whether a reminder is awaiting acknowledgment
// (pending or snoozed).
func IsActionable(rs state.ReminderState) bool {
	return rs.State == state.StatePending || rs.State == state.StateSnoozed
}

// IsSnoozed reports whether a reminder is currently snoozed.
func IsSnoozed(rs state.ReminderState) bool {
	return rs.State == state.StateSnoozed
}

// Dismiss clears a pending or snoozed fire, returning the reminder to the
// acknowledged state with its overdue count and snooze cleared. Reminders in
// any other state are returned unchanged.
func Dismiss(rs state.ReminderState) state.ReminderState {
	if !IsActionable(rs) {
		return rs
	}
	rs.State = state.StateAcknowledged
	rs.SnoozedUntil = nil
	rs.OverdueCount = 0
	return rs
}

// Fire records that a reminder has been triggered. If the reminder is
// already pending or snoozed, the overdue count is incremented (stacking).
// Otherwise it transitions to pending with overdue count 1. Disabled
// reminders never fire.
func (h *Handler) Fire(name string) error {
	if h.State.GetReminderState(name).Disabled {
		return nil
	}
	now := time.Now()
	return h.State.UpdateReminderState(name, func(rs *state.ReminderState) {
		if IsActionable(*rs) {
			rs.OverdueCount++
		} else {
			rs.OverdueCount = 1
		}
		rs.State = state.StatePending
		rs.FiredAt = now
		rs.SnoozedUntil = nil
	})
}

// CheckPasses runs the reminder's check command, records the outcome in state,
// and reports whether the reminder should fire. An empty check always passes
// and records nothing. When the check runs, LastCheckExitCode/LastCheckAt are
// persisted regardless of the result.
func (h *Handler) CheckPasses(name, check string) (bool, error) {
	if check == "" {
		return true, nil
	}

	exitCode, runErr := h.Check.Run(check)

	now := time.Now()
	err := h.State.UpdateReminderState(name, func(rs *state.ReminderState) {
		rs.LastCheckExitCode = &exitCode
		rs.LastCheckAt = now
	})
	if err != nil {
		return false, err
	}

	return exitCode == 0 && runErr == nil, nil
}

// Ack marks a reminder as acknowledged, clearing overdue count and snooze.
// Returns false if the reminder is not in a pending or snoozed state.
func (h *Handler) Ack(name string) (acknowledged bool, err error) {
	rs := h.State.GetReminderState(name)
	if !IsActionable(rs) {
		return false, nil
	}

	h.State.SetReminderState(name, Dismiss(rs))
	return true, h.State.Save()
}

// Snooze marks a reminder as snoozed until the given time.
// Returns an error if the reminder is not in a pending or snoozed state.
func (h *Handler) Snooze(name string, until time.Time) error {
	rs := h.State.GetReminderState(name)
	if !IsActionable(rs) {
		return &InvalidStateError{Name: name, State: rs.State}
	}

	rs.State = state.StateSnoozed
	rs.SnoozedUntil = &until

	h.State.SetReminderState(name, rs)
	return h.State.Save()
}

// InvalidStateError is returned when a state transition is not valid.
type InvalidStateError struct {
	Name  string
	State state.ReminderStatus
}

func (e *InvalidStateError) Error() string {
	return "reminder '" + e.Name + "' is not pending or snoozed (current state: " + e.State.String() + ")"
}
