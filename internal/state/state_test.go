package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestState_LoadSave(t *testing.T) {
	dir := t.TempDir()

	s, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	now := time.Now().Truncate(time.Second) // truncate for JSON round-trip

	s.SetTaskState("backup", TaskState{
		LastRun:        now,
		LastExitCode:   0,
		LastDurationMs: 42000,
	})

	s.SetReminderState("review", ReminderState{
		State:   "pending",
		FiredAt: now,
	})

	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load into fresh instance
	s2, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState (reload): %v", err)
	}

	ts := s2.GetTaskState("backup")
	if ts.LastExitCode != 0 {
		t.Fatalf("Expected exit code 0, got %d", ts.LastExitCode)
	}
	if ts.LastDurationMs != 42000 {
		t.Fatalf("Expected duration 42000, got %d", ts.LastDurationMs)
	}

	rs := s2.GetReminderState("review")
	if rs.State != StatePending {
		t.Fatalf("Expected state 'pending', got %q", rs.State)
	}
}

func TestState_PendingCount(t *testing.T) {
	dir := t.TempDir()
	s, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	if s.Pending() != 0 {
		t.Fatalf("Initial pending should be 0, got %d", s.Pending())
	}

	// Add pending
	s.SetReminderState("r1", ReminderState{State: StatePending, FiredAt: time.Now()})
	if s.Pending() != 1 {
		t.Fatalf("Expected pending 1, got %d", s.Pending())
	}

	s.SetReminderState("r2", ReminderState{State: StatePending, FiredAt: time.Now()})
	if s.Pending() != 2 {
		t.Fatalf("Expected pending 2, got %d", s.Pending())
	}

	// Acknowledge one
	s.SetReminderState("r1", ReminderState{State: StateAcknowledged})
	if s.Pending() != 1 {
		t.Fatalf("Expected pending 1 after ack, got %d", s.Pending())
	}

	// Snooze the other (not pending)
	s.SetReminderState("r2", ReminderState{State: StateSnoozed})
	if s.Pending() != 0 {
		t.Fatalf("Expected pending 0 after snooze, got %d", s.Pending())
	}
}

func TestState_TaskStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	// Set via SetTaskState, verify via GetTaskState
	s.SetTaskState("t1", TaskState{
		LastExitCode:        1,
		RetryAttempt:        2,
		ConsecutiveFailures: 3,
	})

	ts := s.GetTaskState("t1")
	if ts.RetryAttempt != 2 {
		t.Fatalf("Expected retry 2, got %d", ts.RetryAttempt)
	}
	if ts.ConsecutiveFailures != 3 {
		t.Fatalf("Expected 3 failures, got %d", ts.ConsecutiveFailures)
	}

	// Overwrite with reset values
	ts.RetryAttempt = 0
	ts.ConsecutiveFailures = 0
	s.SetTaskState("t1", ts)

	ts = s.GetTaskState("t1")
	if ts.RetryAttempt != 0 {
		t.Fatalf("Expected retry 0, got %d", ts.RetryAttempt)
	}
	if ts.ConsecutiveFailures != 0 {
		t.Fatalf("Expected 0 failures, got %d", ts.ConsecutiveFailures)
	}
}

func TestState_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	s, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	s.SetTaskState("t1", TaskState{LastExitCode: 0, LastDurationMs: 10000})

	// Save multiple times
	for i := range 5 {
		if err := s.Save(); err != nil {
			t.Fatalf("Save attempt %d: %v", i, err)
		}
	}

	// Verify no temp files left behind
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if ext == ".tmp" {
			t.Fatalf("Temp file left behind: %s", e.Name())
		}
	}

	// Verify data survived
	s2, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState (reload): %v", err)
	}
	if s2.GetTaskState("t1").LastDurationMs != 10000 {
		t.Fatal("Data lost after atomic writes")
	}
}

func TestState_SentinelFile(t *testing.T) {
	dir := t.TempDir()
	s, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	sentinelPath := filepath.Join(dir, "pending")

	// Initially no sentinel
	if _, err := os.Stat(sentinelPath); !os.IsNotExist(err) {
		t.Fatal("Sentinel file should not exist initially")
	}

	// Add pending reminder and save
	s.SetReminderState("r1", ReminderState{State: StatePending, FiredAt: time.Now()})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("Expected sentinel file to exist: %v", err)
	}
	if string(data) != "1" {
		t.Fatalf("Expected sentinel content '1', got %q", string(data))
	}

	// Acknowledge and save
	s.SetReminderState("r1", ReminderState{State: StateAcknowledged})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := os.Stat(sentinelPath); !os.IsNotExist(err) {
		t.Fatal("Sentinel file should be removed when no pending reminders")
	}
}

func TestState_OverdueCount(t *testing.T) {
	dir := t.TempDir()
	s, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	// First fire
	s.SetReminderState("r1", ReminderState{
		State:        "pending",
		FiredAt:      time.Now(),
		OverdueCount: 1,
	})

	rs := s.GetReminderState("r1")
	if rs.OverdueCount != 1 {
		t.Fatalf("Expected overdue count 1, got %d", rs.OverdueCount)
	}

	// Simulate stacking: fired again while still pending
	rs.OverdueCount++
	rs.FiredAt = time.Now()
	s.SetReminderState("r1", rs)

	rs = s.GetReminderState("r1")
	if rs.OverdueCount != 2 {
		t.Fatalf("Expected overdue count 2, got %d", rs.OverdueCount)
	}

	// Acknowledge resets
	rs.State = StateAcknowledged
	rs.OverdueCount = 0
	s.SetReminderState("r1", rs)

	rs = s.GetReminderState("r1")
	if rs.OverdueCount != 0 {
		t.Fatalf("Expected overdue count 0 after ack, got %d", rs.OverdueCount)
	}
}

func TestState_EmptyFile(t *testing.T) {
	dir := t.TempDir()

	// Write an empty state.json
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(""), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState with empty file should not error: %v", err)
	}

	// Verify maps are initialized (can set/get without panic)
	ts := s.GetTaskState("test")
	if ts.LastExitCode != 0 {
		t.Fatal("Expected zero-value TaskState for missing key")
	}
}

func TestState_CorruptFile(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{invalid"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := NewState(dir)
	if err == nil {
		t.Fatal("Expected error for corrupt JSON")
	}
}

func TestState_NullMaps(t *testing.T) {
	dir := t.TempDir()

	// Write JSON with null maps
	data, _ := json.Marshal(stateData{Tasks: nil, Reminders: nil})
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	// Should still work -- maps initialized
	ts := s.GetTaskState("nonexistent")
	if ts.LastExitCode != 0 {
		t.Fatal("Zero value expected")
	}
}

func TestState_ConcurrentSave(t *testing.T) {
	dir := t.TempDir()

	now := time.Now().Truncate(time.Second)

	// Spawn multiple goroutines that each create a state instance, set a task, and save.
	const n = 10
	errs := make(chan error, n)
	var wg sync.WaitGroup
	wg.Add(n)

	for i := range n {
		go func(idx int) {
			defer wg.Done()
			s, err := NewState(dir)
			if err != nil {
				errs <- fmt.Errorf("NewState %d: %w", idx, err)
				return
			}
			s.SetTaskState(fmt.Sprintf("task-%d", idx), TaskState{
				LastRun:      now,
				LastExitCode: idx,
			})
			if err := s.Save(); err != nil {
				errs <- fmt.Errorf("Save %d: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// Load fresh and verify all tasks survived
	s, err := NewState(dir)
	if err != nil {
		t.Fatalf("final NewState: %v", err)
	}

	for i := range n {
		ts := s.GetTaskState(fmt.Sprintf("task-%d", i))
		if ts.LastExitCode != i {
			t.Errorf("task-%d: expected exit code %d, got %d", i, i, ts.LastExitCode)
		}
	}
}

func TestState_DeleteTaskState(t *testing.T) {
	dir := t.TempDir()
	s, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	s.SetTaskState("task1", TaskState{LastExitCode: 1})
	s.SetTaskState("task2", TaskState{LastExitCode: 2})

	s.DeleteTaskState("task1")

	ts := s.GetTaskState("task1")
	if ts.LastExitCode != 0 {
		t.Fatalf("Expected zero value after delete, got exit code %d", ts.LastExitCode)
	}

	// task2 should still exist
	ts2 := s.GetTaskState("task2")
	if ts2.LastExitCode != 2 {
		t.Fatalf("Expected task2 exit code 2, got %d", ts2.LastExitCode)
	}

	// Persist and reload
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s2, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState reload: %v", err)
	}
	if s2.GetTaskState("task1").LastExitCode != 0 {
		t.Fatal("Deleted task should not persist")
	}
	if s2.GetTaskState("task2").LastExitCode != 2 {
		t.Fatal("task2 should persist")
	}
}

func TestState_DeleteReminderState(t *testing.T) {
	dir := t.TempDir()
	s, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	// Add a pending reminder and a non-pending one
	s.SetReminderState("r1", ReminderState{State: StatePending, OverdueCount: 3})
	s.SetReminderState("r2", ReminderState{State: StateAcknowledged})

	if s.Pending() != 1 {
		t.Fatalf("Expected pending=1, got %d", s.Pending())
	}

	// Deleting a pending reminder should decrement Pending
	s.DeleteReminderState("r1")

	if s.Pending() != 0 {
		t.Fatalf("Expected pending=0 after delete, got %d", s.Pending())
	}

	rs := s.GetReminderState("r1")
	if rs.State != "" {
		t.Fatalf("Expected empty state after delete, got %q", rs.State)
	}

	// r2 should still exist
	rs2 := s.GetReminderState("r2")
	if rs2.State != StateAcknowledged {
		t.Fatalf("Expected r2 state acknowledged, got %q", rs2.State)
	}

	// Deleting a non-existent reminder should be a no-op
	s.DeleteReminderState("nonexistent")
}

func TestState_DirtyTracking_ExternalWriteNotOverwritten(t *testing.T) {
	// After Save(), dirty sets are cleared. A subsequent Save() with no new
	// mutations must not overwrite changes made by another process.
	dir := t.TempDir()

	s1, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState s1: %v", err)
	}

	s1.SetTaskState("shared", TaskState{LastExitCode: 1})
	if err := s1.Save(); err != nil {
		t.Fatalf("Save s1: %v", err)
	}

	// External process updates the same key
	s2, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState s2: %v", err)
	}
	s2.SetTaskState("shared", TaskState{LastExitCode: 99})
	if err := s2.Save(); err != nil {
		t.Fatalf("Save s2: %v", err)
	}

	// s1 saves again with NO new mutations — should NOT overwrite s2's value
	if err := s1.Save(); err != nil {
		t.Fatalf("Save s1 again: %v", err)
	}

	s3, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState s3: %v", err)
	}
	ts := s3.GetTaskState("shared")
	if ts.LastExitCode != 99 {
		t.Fatalf("Expected external write (99) to survive, got %d", ts.LastExitCode)
	}
}

func TestState_DirtyTracking_SetThenDelete(t *testing.T) {
	// Set then Delete: key should be deleted on disk after save.
	dir := t.TempDir()

	// Seed a key on disk first
	s1, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	s1.SetTaskState("ephemeral", TaskState{LastExitCode: 1})
	if err := s1.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// New instance: set then delete
	s2, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	s2.SetTaskState("ephemeral", TaskState{LastExitCode: 2})
	s2.DeleteTaskState("ephemeral")
	if err := s2.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s3, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	ts := s3.GetTaskState("ephemeral")
	if ts.LastExitCode != 0 {
		t.Fatalf("Expected key deleted (zero value), got exit code %d", ts.LastExitCode)
	}
}

func TestState_DirtyTracking_DeleteThenSet(t *testing.T) {
	// Delete then Set: key should exist on disk after save.
	dir := t.TempDir()

	s1, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	s1.SetTaskState("revived", TaskState{LastExitCode: 1})
	if err := s1.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s2, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	s2.DeleteTaskState("revived")
	s2.SetTaskState("revived", TaskState{LastExitCode: 42})
	if err := s2.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s3, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	ts := s3.GetTaskState("revived")
	if ts.LastExitCode != 42 {
		t.Fatalf("Expected revived key with exit code 42, got %d", ts.LastExitCode)
	}
}

func TestState_DirtyTracking_MultipleSaves(t *testing.T) {
	// Save, mutate, save again — only the second mutation should be overlaid.
	dir := t.TempDir()

	s1, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	s1.SetTaskState("counter", TaskState{LastExitCode: 1})
	if err := s1.Save(); err != nil {
		t.Fatalf("Save 1: %v", err)
	}

	// External process writes a different key
	s2, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState s2: %v", err)
	}
	s2.SetTaskState("other", TaskState{LastExitCode: 7})
	if err := s2.Save(); err != nil {
		t.Fatalf("Save s2: %v", err)
	}

	// s1 mutates counter again and saves
	s1.SetTaskState("counter", TaskState{LastExitCode: 2})
	if err := s1.Save(); err != nil {
		t.Fatalf("Save 2: %v", err)
	}

	s3, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState s3: %v", err)
	}
	if s3.GetTaskState("counter").LastExitCode != 2 {
		t.Fatalf("Expected counter=2, got %d", s3.GetTaskState("counter").LastExitCode)
	}
	if s3.GetTaskState("other").LastExitCode != 7 {
		t.Fatalf("Expected other=7, got %d", s3.GetTaskState("other").LastExitCode)
	}
}

func TestState_DirtyTracking_DeleteSurvivesConcurrentSave(t *testing.T) {
	// Process A deletes a key. Process B writes a different key.
	// After both save, the deleted key should be gone.
	dir := t.TempDir()

	// Seed with two keys
	s0, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	s0.SetTaskState("keep", TaskState{LastExitCode: 1})
	s0.SetTaskState("remove", TaskState{LastExitCode: 2})
	if err := s0.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s1, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState s1: %v", err)
	}
	s2, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState s2: %v", err)
	}

	s1.DeleteTaskState("remove")
	s2.SetTaskState("keep", TaskState{LastExitCode: 99})

	if err := s1.Save(); err != nil {
		t.Fatalf("Save s1: %v", err)
	}
	if err := s2.Save(); err != nil {
		t.Fatalf("Save s2: %v", err)
	}

	s3, err := NewState(dir)
	if err != nil {
		t.Fatalf("NewState s3: %v", err)
	}
	if s3.GetTaskState("remove").LastExitCode != 0 {
		t.Fatal("Deleted key 'remove' should not exist after concurrent save")
	}
	if s3.GetTaskState("keep").LastExitCode != 99 {
		t.Fatalf("Expected keep=99, got %d", s3.GetTaskState("keep").LastExitCode)
	}
}

func TestReminderState_CheckFields(t *testing.T) {
	dir := t.TempDir()
	s, err := NewState(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Set reminder with check fields
	exitCode := 1
	now := time.Now().Truncate(time.Millisecond)
	s.SetReminderState("pacnew", ReminderState{
		State:             StatePending,
		FiredAt:           now,
		OverdueCount:      1,
		LastCheckExitCode: &exitCode,
		LastCheckAt:       now,
	})

	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// Reload and verify
	s2, err := NewState(dir)
	if err != nil {
		t.Fatal(err)
	}

	rs := s2.GetReminderState("pacnew")
	if rs.LastCheckExitCode == nil {
		t.Fatal("Expected LastCheckExitCode to be non-nil")
	}
	if *rs.LastCheckExitCode != 1 {
		t.Fatalf("Expected LastCheckExitCode=1, got %d", *rs.LastCheckExitCode)
	}
	if !rs.LastCheckAt.Equal(now) {
		t.Fatalf("Expected LastCheckAt=%v, got %v", now, rs.LastCheckAt)
	}

	// Update with successful check
	exitCode0 := 0
	rs.LastCheckExitCode = &exitCode0
	s2.SetReminderState("pacnew", rs)
	if err := s2.Save(); err != nil {
		t.Fatal(err)
	}

	s3, err := NewState(dir)
	if err != nil {
		t.Fatal(err)
	}
	rs3 := s3.GetReminderState("pacnew")
	if *rs3.LastCheckExitCode != 0 {
		t.Fatalf("Expected LastCheckExitCode=0 after update, got %d", *rs3.LastCheckExitCode)
	}
}

func TestAppliedConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewState(dir)
	if err != nil {
		t.Fatal(err)
	}

	ac := &AppliedConfig{
		Tasks: map[string]AppliedTaskConfig{
			"backup": {
				Command:  "rsync -av ~/Documents /mnt/backup/",
				Schedule: "daily",
				OnMissed: "run_once",
				Retry:    AppliedRetryConfig{Attempts: 3, Delay: "5m"},
			},
			"deploy": {
				Command: "./deploy.sh",
			},
		},
		Reminders: map[string]AppliedReminderConfig{
			"weekly-review": {
				Command:  "echo review",
				Schedule: "weekly",
				Message:  "Time for your weekly review",
				Snooze:   "2h",
			},
			"check-pacnew": {
				Schedule: "daily",
				Message:  "There are pacnew files!",
				Check:    "locate .pacnew",
			},
		},
	}

	s.SetAppliedConfig(ac)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// Reload from disk
	s2, err := NewState(dir)
	if err != nil {
		t.Fatal(err)
	}

	got := s2.GetAppliedConfig()
	if got == nil {
		t.Fatal("expected non-nil applied config after reload")
	}

	// Verify tasks
	if len(got.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(got.Tasks))
	}
	backup := got.Tasks["backup"]
	if backup.Command != "rsync -av ~/Documents /mnt/backup/" {
		t.Errorf("backup command = %q", backup.Command)
	}
	if backup.Retry.Attempts != 3 || backup.Retry.Delay != "5m" {
		t.Errorf("backup retry = %+v", backup.Retry)
	}

	// Verify reminders
	if len(got.Reminders) != 2 {
		t.Fatalf("expected 2 reminders, got %d", len(got.Reminders))
	}
	pacnew := got.Reminders["check-pacnew"]
	if pacnew.Check != "locate .pacnew" {
		t.Errorf("pacnew check = %q", pacnew.Check)
	}
}

func TestDisabled_Persistence(t *testing.T) {
	dir := t.TempDir()
	s, err := NewState(dir)
	if err != nil {
		t.Fatal(err)
	}

	s.SetTaskState("mytask", TaskState{Disabled: true, LastExitCode: 0})
	s.SetReminderState("myreminder", ReminderState{Disabled: true, State: StatePending})

	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	s2, err := NewState(dir)
	if err != nil {
		t.Fatal(err)
	}

	ts := s2.GetTaskState("mytask")
	if !ts.Disabled {
		t.Error("expected task to remain disabled after reload")
	}

	rs := s2.GetReminderState("myreminder")
	if !rs.Disabled {
		t.Error("expected reminder to remain disabled after reload")
	}
}

func TestAppliedConfig_Overwrite(t *testing.T) {
	dir := t.TempDir()
	s, err := NewState(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Set initial config
	s.SetAppliedConfig(&AppliedConfig{
		Tasks: map[string]AppliedTaskConfig{
			"old-task": {Command: "echo old", Schedule: "daily"},
		},
	})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// Overwrite with new config
	s2, err := NewState(dir)
	if err != nil {
		t.Fatal(err)
	}
	s2.SetAppliedConfig(&AppliedConfig{
		Tasks: map[string]AppliedTaskConfig{
			"new-task": {Command: "echo new", Schedule: "weekly"},
		},
	})
	if err := s2.Save(); err != nil {
		t.Fatal(err)
	}

	// Verify old is gone, new is present
	s3, err := NewState(dir)
	if err != nil {
		t.Fatal(err)
	}
	ac := s3.GetAppliedConfig()
	if _, ok := ac.Tasks["old-task"]; ok {
		t.Error("old-task should not exist after overwrite")
	}
	if _, ok := ac.Tasks["new-task"]; !ok {
		t.Fatal("new-task should exist after overwrite")
	}
	if ac.Tasks["new-task"].Schedule != "weekly" {
		t.Errorf("expected weekly, got %s", ac.Tasks["new-task"].Schedule)
	}
}

func TestAppliedConfig_Classify(t *testing.T) {
	ac := &AppliedConfig{
		Tasks:     map[string]AppliedTaskConfig{"backup": {}, "both": {}},
		Reminders: map[string]AppliedReminderConfig{"review": {}, "both": {}},
	}

	tests := []struct {
		name              string
		wantTask, wantRem bool
	}{
		{"backup", true, false}, // task only
		{"review", false, true}, // reminder only
		{"both", true, true},    // shared name → both
		{"missing", false, false},
	}
	for _, tc := range tests {
		gotTask, gotRem := ac.Classify(tc.name)
		if gotTask != tc.wantTask || gotRem != tc.wantRem {
			t.Errorf("Classify(%q) = (%v, %v), want (%v, %v)", tc.name, gotTask, gotRem, tc.wantTask, tc.wantRem)
		}
	}

	// A nil config classifies everything as absent.
	var nilConfig *AppliedConfig
	if gotTask, gotRem := nilConfig.Classify("x"); gotTask || gotRem {
		t.Errorf("nil.Classify() = (%v, %v), want (false, false)", gotTask, gotRem)
	}
}

func TestAppliedConfig_Names(t *testing.T) {
	ac := &AppliedConfig{
		Tasks:     map[string]AppliedTaskConfig{"t1": {}, "t2": {}},
		Reminders: map[string]AppliedReminderConfig{"r1": {}},
	}

	if got := len(ac.Names()); got != 3 {
		t.Errorf("Names() len = %d, want 3", got)
	}
	if got := len(ac.TaskNames()); got != 2 {
		t.Errorf("TaskNames() len = %d, want 2", got)
	}
	if got := len(ac.ReminderNames()); got != 1 {
		t.Errorf("ReminderNames() len = %d, want 1", got)
	}

	var nilConfig *AppliedConfig
	if nilConfig.Names() != nil || nilConfig.TaskNames() != nil || nilConfig.ReminderNames() != nil {
		t.Error("nil config should return nil name slices")
	}
}
