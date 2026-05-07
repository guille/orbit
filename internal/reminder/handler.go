// Package reminder encapsulates reminder state transitions.
package reminder

import (
	"time"

	"github.com/guille/orbit/internal/state"
)

// StateTracker defines the interface for state operations needed by Handler.
type StateTracker interface {
	GetReminderState(name string) state.ReminderState
	SetReminderState(name string, rs state.ReminderState)
	Save() error
}

// Handler manages reminder state transitions.
type Handler struct {
	State StateTracker
}

// NewHandler creates a new reminder handler.
func NewHandler(s StateTracker) *Handler {
	return &Handler{State: s}
}

// Fire records that a reminder has been triggered. If the reminder is
// already pending or snoozed, the overdue count is incremented (stacking).
// Otherwise it transitions to pending with overdue count 1.
func (h *Handler) Fire(name string) error {
	rs := h.State.GetReminderState(name)
	now := time.Now()

	if rs.State == state.StatePending || rs.State == state.StateSnoozed {
		rs.OverdueCount++
	} else {
		rs.OverdueCount = 1
	}
	rs.State = state.StatePending
	rs.FiredAt = now
	rs.SnoozedUntil = nil

	h.State.SetReminderState(name, rs)
	return h.State.Save()
}

// Ack marks a reminder as acknowledged, clearing overdue count and snooze.
// Returns false if the reminder is not in a pending or snoozed state.
func (h *Handler) Ack(name string) (acknowledged bool, err error) {
	rs := h.State.GetReminderState(name)
	if rs.State != state.StatePending && rs.State != state.StateSnoozed {
		return false, nil
	}

	rs.State = state.StateAcknowledged
	rs.SnoozedUntil = nil
	rs.OverdueCount = 0

	h.State.SetReminderState(name, rs)
	return true, h.State.Save()
}

// Snooze marks a reminder as snoozed until the given time.
// Returns an error if the reminder is not in a pending or snoozed state.
func (h *Handler) Snooze(name string, until time.Time) error {
	rs := h.State.GetReminderState(name)
	if rs.State != state.StatePending && rs.State != state.StateSnoozed {
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
