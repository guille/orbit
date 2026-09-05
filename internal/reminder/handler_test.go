package reminder

import (
	"errors"
	"testing"
	"time"

	"go.guillerg.dev/orbit/internal/state"
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

func (m *mockState) UpdateReminderState(name string, fn func(*state.ReminderState)) error {
	rs := m.reminders[name]
	fn(&rs)
	m.reminders[name] = rs
	m.saved = true
	return nil
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

func TestHandler_Fire_Disabled(t *testing.T) {
	ms := newMockState()
	ms.reminders["test"] = state.ReminderState{State: state.StateAcknowledged, Disabled: true}
	h := NewHandler(ms)

	if err := h.Fire("test"); err != nil {
		t.Fatalf("Fire: %v", err)
	}

	rs := ms.reminders["test"]
	if rs.State != state.StateAcknowledged {
		t.Errorf("disabled reminder should not fire, got state %q", rs.State)
	}
	if ms.saved {
		t.Error("expected no save when a disabled reminder is fired")
	}
}

func TestDismiss(t *testing.T) {
	until := time.Now().Add(time.Hour)
	tests := []struct {
		name string
		in   state.ReminderState
		want state.ReminderStatus
		// clears reports whether snooze/overdue should be cleared.
		clears bool
	}{
		{"pending", state.ReminderState{State: state.StatePending, OverdueCount: 3}, state.StateAcknowledged, true},
		{"snoozed", state.ReminderState{State: state.StateSnoozed, SnoozedUntil: &until, OverdueCount: 2}, state.StateAcknowledged, true},
		{"acknowledged", state.ReminderState{State: state.StateAcknowledged}, state.StateAcknowledged, false},
		{"new", state.ReminderState{}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Dismiss(tc.in)
			if got.State != tc.want {
				t.Errorf("state = %q, want %q", got.State, tc.want)
			}
			if tc.clears {
				if got.SnoozedUntil != nil {
					t.Error("expected SnoozedUntil cleared")
				}
				if got.OverdueCount != 0 {
					t.Errorf("expected overdue 0, got %d", got.OverdueCount)
				}
			}
		})
	}
}

func TestDismiss_PreservesDisabled(t *testing.T) {
	got := Dismiss(state.ReminderState{State: state.StatePending, Disabled: true})
	if !got.Disabled {
		t.Error("Dismiss should preserve the Disabled flag")
	}
	if got.State != state.StateAcknowledged {
		t.Errorf("expected acknowledged, got %q", got.State)
	}
}

type fakeCheck struct {
	exitCode int
	err      error
	ran      bool
}

func (f *fakeCheck) Run(string) (int, error) {
	f.ran = true
	return f.exitCode, f.err
}

func TestHandler_CheckPasses_Empty(t *testing.T) {
	ms := newMockState()
	fc := &fakeCheck{}
	h := &Handler{State: ms, Check: fc}

	ok, err := h.CheckPasses("test", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("empty check should pass")
	}
	if fc.ran {
		t.Error("empty check should not invoke the runner")
	}
	if ms.saved {
		t.Error("empty check should not touch state")
	}
	if ms.reminders["test"].LastCheckExitCode != nil {
		t.Error("empty check should not record an exit code")
	}
}

func TestHandler_CheckPasses_Success(t *testing.T) {
	ms := newMockState()
	h := &Handler{State: ms, Check: &fakeCheck{exitCode: 0}}

	ok, err := h.CheckPasses("test", "true")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("exit 0 should pass")
	}
	rs := ms.reminders["test"]
	if rs.LastCheckExitCode == nil || *rs.LastCheckExitCode != 0 {
		t.Errorf("expected recorded exit code 0, got %v", rs.LastCheckExitCode)
	}
	if rs.LastCheckAt.IsZero() {
		t.Error("expected LastCheckAt to be set")
	}
	if !ms.saved {
		t.Error("expected Save to be called")
	}
}

func TestHandler_CheckPasses_Failure(t *testing.T) {
	ms := newMockState()
	h := &Handler{State: ms, Check: &fakeCheck{exitCode: 3}}

	ok, err := h.CheckPasses("test", "false")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("non-zero exit should not pass")
	}
	rs := ms.reminders["test"]
	if rs.LastCheckExitCode == nil || *rs.LastCheckExitCode != 3 {
		t.Errorf("expected recorded exit code 3, got %v", rs.LastCheckExitCode)
	}
	if !ms.saved {
		t.Error("expected Save to be called even on failure")
	}
}

func TestHandler_CheckPasses_RunError(t *testing.T) {
	ms := newMockState()
	// Command couldn't be started: suppress the reminder but record the outcome.
	h := &Handler{State: ms, Check: &fakeCheck{exitCode: 1, err: errors.New("exec: not found")}}

	ok, err := h.CheckPasses("test", "bogus")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a start error should not pass")
	}
	if !ms.saved {
		t.Error("expected Save to be called")
	}
}
