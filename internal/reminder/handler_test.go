package reminder

import (
	"testing"
	"time"

	"github.com/guille/orbit/internal/state"
)

type mockState struct {
	reminders map[string]state.ReminderState
	saved     bool
}

func newMockState() *mockState {
	return &mockState{reminders: make(map[string]state.ReminderState)}
}

func (m *mockState) GetReminderState(name string) state.ReminderState {
	return m.reminders[name]
}

func (m *mockState) SetReminderState(name string, rs state.ReminderState) {
	m.reminders[name] = rs
}

func (m *mockState) Save() error {
	m.saved = true
	return nil
}

func TestHandler_Fire_NewReminder(t *testing.T) {
	ms := newMockState()
	h := NewHandler(ms)

	if err := h.Fire("test"); err != nil {
		t.Fatalf("Fire: %v", err)
	}

	rs := ms.reminders["test"]
	if rs.State != state.StatePending {
		t.Fatalf("expected pending, got %q", rs.State)
	}
	if rs.OverdueCount != 1 {
		t.Fatalf("expected overdue=1, got %d", rs.OverdueCount)
	}
	if rs.FiredAt.IsZero() {
		t.Fatal("expected FiredAt to be set")
	}
	if !ms.saved {
		t.Fatal("expected Save to be called")
	}
}

func TestHandler_Fire_Stacking(t *testing.T) {
	ms := newMockState()
	ms.reminders["test"] = state.ReminderState{
		State:        state.StatePending,
		OverdueCount: 3,
	}
	h := NewHandler(ms)

	if err := h.Fire("test"); err != nil {
		t.Fatalf("Fire: %v", err)
	}

	rs := ms.reminders["test"]
	if rs.OverdueCount != 4 {
		t.Fatalf("expected overdue=4, got %d", rs.OverdueCount)
	}
}

func TestHandler_Fire_SnoozedStacking(t *testing.T) {
	ms := newMockState()
	snoozeTime := time.Now().Add(time.Hour)
	ms.reminders["test"] = state.ReminderState{
		State:        state.StateSnoozed,
		OverdueCount: 5,
		SnoozedUntil: &snoozeTime,
	}
	h := NewHandler(ms)

	if err := h.Fire("test"); err != nil {
		t.Fatalf("Fire: %v", err)
	}

	rs := ms.reminders["test"]
	if rs.OverdueCount != 6 {
		t.Fatalf("expected overdue=6 (increment from snoozed), got %d", rs.OverdueCount)
	}
	if rs.State != state.StatePending {
		t.Fatalf("expected state to transition to pending, got %q", rs.State)
	}
	if rs.SnoozedUntil != nil {
		t.Fatal("expected SnoozedUntil to be cleared")
	}
}

func TestHandler_Ack(t *testing.T) {
	ms := newMockState()
	ms.reminders["test"] = state.ReminderState{
		State:        state.StatePending,
		OverdueCount: 3,
	}
	h := NewHandler(ms)

	acked, err := h.Ack("test")
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if !acked {
		t.Fatal("expected acknowledged=true")
	}

	rs := ms.reminders["test"]
	if rs.State != state.StateAcknowledged {
		t.Fatalf("expected acknowledged, got %q", rs.State)
	}
	if rs.OverdueCount != 0 {
		t.Fatalf("expected overdue=0, got %d", rs.OverdueCount)
	}
}

func TestHandler_Ack_NotPending(t *testing.T) {
	ms := newMockState()
	ms.reminders["test"] = state.ReminderState{State: state.StateAcknowledged}
	h := NewHandler(ms)

	acked, err := h.Ack("test")
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if acked {
		t.Fatal("expected acknowledged=false for already-acknowledged reminder")
	}
}

func TestHandler_Snooze(t *testing.T) {
	ms := newMockState()
	ms.reminders["test"] = state.ReminderState{
		State:        state.StatePending,
		OverdueCount: 2,
	}
	h := NewHandler(ms)

	until := time.Now().Add(2 * time.Hour)
	if err := h.Snooze("test", until); err != nil {
		t.Fatalf("Snooze: %v", err)
	}

	rs := ms.reminders["test"]
	if rs.State != state.StateSnoozed {
		t.Fatalf("expected snoozed, got %q", rs.State)
	}
	if rs.SnoozedUntil == nil || !rs.SnoozedUntil.Equal(until) {
		t.Fatal("expected SnoozedUntil to match")
	}
	// Overdue count should be preserved (not cleared on snooze)
	if rs.OverdueCount != 2 {
		t.Fatalf("expected overdue=2 preserved, got %d", rs.OverdueCount)
	}
}

func TestHandler_Snooze_InvalidState(t *testing.T) {
	ms := newMockState()
	ms.reminders["test"] = state.ReminderState{State: state.StateAcknowledged}
	h := NewHandler(ms)

	err := h.Snooze("test", time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("expected error for snoozing acknowledged reminder")
	}
	if _, ok := err.(*InvalidStateError); !ok {
		t.Fatalf("expected InvalidStateError, got %T", err)
	}
}

func TestHandler_Ack_NeverFired(t *testing.T) {
	ms := newMockState()
	h := NewHandler(ms)

	// Ack on a reminder that was never fired (empty state)
	acked, err := h.Ack("never-fired")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acked {
		t.Error("expected ack to return false for never-fired reminder")
	}
	if ms.saved {
		t.Error("expected no save when nothing was acked")
	}
}

func TestHandler_Ack_Snoozed(t *testing.T) {
	ms := newMockState()
	h := NewHandler(ms)

	until := time.Now().Add(time.Hour)
	ms.reminders["test"] = state.ReminderState{
		State:        state.StateSnoozed,
		SnoozedUntil: &until,
		OverdueCount: 3,
	}

	acked, err := h.Ack("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !acked {
		t.Error("expected ack to succeed on snoozed reminder")
	}

	rs := ms.reminders["test"]
	if rs.State != state.StateAcknowledged {
		t.Errorf("expected acknowledged, got %s", rs.State)
	}
	if rs.SnoozedUntil != nil {
		t.Error("expected SnoozedUntil cleared")
	}
	if rs.OverdueCount != 0 {
		t.Errorf("expected overdue count 0, got %d", rs.OverdueCount)
	}
}

func TestHandler_Snooze_AlreadySnoozed(t *testing.T) {
	ms := newMockState()
	h := NewHandler(ms)

	first := time.Now().Add(time.Hour)
	ms.reminders["test"] = state.ReminderState{
		State:        state.StateSnoozed,
		SnoozedUntil: &first,
		OverdueCount: 2,
	}

	second := time.Now().Add(2 * time.Hour)
	err := h.Snooze("test", second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rs := ms.reminders["test"]
	if rs.State != state.StateSnoozed {
		t.Errorf("expected snoozed, got %s", rs.State)
	}
	if rs.SnoozedUntil == nil || !rs.SnoozedUntil.Equal(second) {
		t.Error("expected SnoozedUntil updated to new time")
	}
	// Overdue count should be preserved across re-snooze
	if rs.OverdueCount != 2 {
		t.Errorf("expected overdue count preserved at 2, got %d", rs.OverdueCount)
	}
}

func TestHandler_Fire_OnAcknowledged(t *testing.T) {
	ms := newMockState()
	h := NewHandler(ms)

	ms.reminders["test"] = state.ReminderState{
		State:        state.StateAcknowledged,
		OverdueCount: 0,
	}

	if err := h.Fire("test"); err != nil {
		t.Fatal(err)
	}

	rs := ms.reminders["test"]
	if rs.State != state.StatePending {
		t.Errorf("expected pending, got %s", rs.State)
	}
	if rs.OverdueCount != 1 {
		t.Errorf("expected overdue count reset to 1, got %d", rs.OverdueCount)
	}
}
