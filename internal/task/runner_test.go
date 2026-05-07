package task

import (
	"errors"
	"testing"
	"time"

	"github.com/guille/orbit/internal/config"
	"github.com/guille/orbit/internal/state"
)

// MockStateTracker is a simplified mock of the StateTracker interface for testing.
type MockStateTracker struct {
	TaskStates map[string]state.TaskState
	SaveCalled int
	SaveError  error
}

func NewMockStateTracker() *MockStateTracker {
	return &MockStateTracker{
		TaskStates: make(map[string]state.TaskState),
	}
}

func (m *MockStateTracker) GetTaskState(name string) state.TaskState {
	return m.TaskStates[name]
}

func (m *MockStateTracker) SetTaskState(name string, ts state.TaskState) {
	m.TaskStates[name] = ts
}

func (m *MockStateTracker) Save() error {
	m.SaveCalled++
	return m.SaveError
}

func TestTaskRunner_Success(t *testing.T) {
	mock := NewMockStateTracker()
	runner := NewRunner(mock)

	err := runner.Run("test-task", "exit 0", config.RetryConfig{Attempts: new(3), Delay: "1ms"})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	ts := mock.GetTaskState("test-task")
	if ts.LastExitCode != 0 {
		t.Fatalf("Expected exit code 0, got %d", ts.LastExitCode)
	}
	if ts.ConsecutiveFailures != 0 {
		t.Fatalf("Expected 0 consecutive failures, got %d", ts.ConsecutiveFailures)
	}
	if ts.RetryAttempt != 0 {
		t.Fatalf("Expected retry attempt 0, got %d", ts.RetryAttempt)
	}
	if mock.SaveCalled != 1 {
		t.Fatalf("Expected Save() called 1 time, got %d", mock.SaveCalled)
	}
}

func TestTaskRunner_Failure_NoRetry(t *testing.T) {
	mock := NewMockStateTracker()
	runner := NewRunner(mock)

	// Attempts=0 means run once (no retries)
	err := runner.Run("test-task", "exit 1", config.RetryConfig{Attempts: new(0), Delay: "1ms"})
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	ts := mock.GetTaskState("test-task")
	if ts.LastExitCode != 1 {
		t.Fatalf("Expected exit code 1, got %d", ts.LastExitCode)
	}
	if ts.ConsecutiveFailures != 1 {
		t.Fatalf("Expected 1 consecutive failure, got %d", ts.ConsecutiveFailures)
	}
	if ts.RetryAttempt != 1 {
		t.Fatalf("Expected retry attempt 1, got %d", ts.RetryAttempt)
	}
	if mock.SaveCalled != 1 {
		t.Fatalf("Expected Save() called 1 time, got %d", mock.SaveCalled)
	}
}

func TestTaskRunner_Failure_WithRetry_AllFail(t *testing.T) {
	mock := NewMockStateTracker()
	runner := NewRunner(mock)

	// 3 attempts, all will fail -- the retry loop runs all 3 in one call
	err := runner.Run("test-task", "exit 1", config.RetryConfig{Attempts: new(3), Delay: "1ms"})
	if err == nil {
		t.Fatal("Expected error after all retries exhausted, got nil")
	}

	ts := mock.GetTaskState("test-task")
	if ts.LastExitCode != 1 {
		t.Fatalf("Expected exit code 1, got %d", ts.LastExitCode)
	}
	if ts.ConsecutiveFailures != 3 {
		t.Fatalf("Expected 3 consecutive failures, got %d", ts.ConsecutiveFailures)
	}
	if ts.RetryAttempt != 3 {
		t.Fatalf("Expected retry attempt 3, got %d", ts.RetryAttempt)
	}
	// Save called once per attempt
	if mock.SaveCalled != 3 {
		t.Fatalf("Expected Save() called 3 times, got %d", mock.SaveCalled)
	}
}

func TestTaskRunner_Failure_ThenSuccess(t *testing.T) {
	mock := NewMockStateTracker()
	runner := NewRunner(mock)

	// Use a script that fails the first time, succeeds second time
	// We'll simulate this by running two separate Run() calls: first a single-attempt
	// failure, then manually resume with a succeeding command.
	err := runner.Run("test-task", "exit 1", config.RetryConfig{Attempts: new(1), Delay: "1ms"})
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	ts := mock.GetTaskState("test-task")
	if ts.ConsecutiveFailures != 1 {
		t.Fatalf("Expected 1 consecutive failure, got %d", ts.ConsecutiveFailures)
	}

	// Now succeed
	err = runner.Run("test-task", "exit 0", config.RetryConfig{Attempts: new(1), Delay: "1ms"})
	if err != nil {
		t.Fatalf("Expected success, got: %v", err)
	}

	ts = mock.GetTaskState("test-task")
	if ts.LastExitCode != 0 {
		t.Fatalf("Expected exit code 0, got %d", ts.LastExitCode)
	}
	if ts.ConsecutiveFailures != 0 {
		t.Fatalf("Expected 0 consecutive failures after success, got %d", ts.ConsecutiveFailures)
	}
	if ts.RetryAttempt != 0 {
		t.Fatalf("Expected retry attempt 0 after success, got %d", ts.RetryAttempt)
	}
}

func TestTaskRunner_RetryResetAfterExhaustion(t *testing.T) {
	mock := NewMockStateTracker()
	runner := NewRunner(mock)

	// Exhaust 3 retries
	err := runner.Run("test-task", "exit 1", config.RetryConfig{Attempts: new(3), Delay: "1ms"})
	if err == nil {
		t.Fatal("Expected error")
	}

	ts := mock.GetTaskState("test-task")
	if ts.RetryAttempt != 3 {
		t.Fatalf("Expected retry attempt 3, got %d", ts.RetryAttempt)
	}

	// Next invocation should reset retry attempt but preserve consecutive failures
	mock.SaveCalled = 0
	err = runner.Run("test-task", "exit 1", config.RetryConfig{Attempts: new(3), Delay: "1ms"})
	if err == nil {
		t.Fatal("Expected error")
	}

	ts = mock.GetTaskState("test-task")
	// RetryAttempt should have been reset to 0 then incremented 3 times
	if ts.RetryAttempt != 3 {
		t.Fatalf("Expected retry attempt 3 after reset, got %d", ts.RetryAttempt)
	}
	// ConsecutiveFailures accumulates across retry cycles (only resets on success)
	if ts.ConsecutiveFailures != 6 {
		t.Fatalf("Expected 6 consecutive failures (accumulated across cycles), got %d", ts.ConsecutiveFailures)
	}
	if mock.SaveCalled != 3 {
		t.Fatalf("Expected Save() called 3 times after reset, got %d", mock.SaveCalled)
	}
}

func TestTaskRunner_CommandNotFound(t *testing.T) {
	mock := NewMockStateTracker()
	runner := NewRunner(mock)

	err := runner.Run("test-task", "this-command-does-not-exist-12345", config.RetryConfig{Attempts: new(1), Delay: "1ms"})
	if err == nil {
		t.Fatal("Expected error for non-existent command, got nil")
	}

	ts := mock.GetTaskState("test-task")
	if ts.LastExitCode == 0 {
		t.Fatal("Expected non-zero exit code")
	}
	if ts.ConsecutiveFailures != 1 {
		t.Fatalf("Expected 1 consecutive failure, got %d", ts.ConsecutiveFailures)
	}
}

func TestTaskRunner_EmptyCommand(t *testing.T) {
	mock := NewMockStateTracker()
	runner := NewRunner(mock)

	err := runner.Run("test-task", "", config.RetryConfig{Attempts: new(1)})
	if err == nil {
		t.Fatal("Expected error for empty command, got nil")
	}
}

func TestTaskRunner_SaveError(t *testing.T) {
	mock := NewMockStateTracker()
	mock.SaveError = errors.New("disk full")
	runner := NewRunner(mock)

	err := runner.Run("test-task", "exit 0", config.RetryConfig{Attempts: new(1), Delay: "1ms"})
	if err == nil {
		t.Fatal("Expected error when save fails, got nil")
	}
	if !errors.Is(err, mock.SaveError) {
		t.Fatalf("Expected save error to be wrapped, got: %v", err)
	}

	// State should still be updated in memory even though save failed
	ts := mock.GetTaskState("test-task")
	if ts.LastExitCode != 0 {
		t.Fatalf("Expected exit code 0 in memory, got %d", ts.LastExitCode)
	}
}

func TestTaskRunner_InvalidDelay(t *testing.T) {
	mock := NewMockStateTracker()
	runner := NewRunner(mock)

	err := runner.Run("test-task", "exit 0", config.RetryConfig{Attempts: new(3), Delay: "not-a-duration"})
	if err == nil {
		t.Fatal("Expected error for invalid delay, got nil")
	}
}

// --- Mock executor tests (no real shell execution) ---

// MockExecutor returns predefined results for each call.
type MockExecutor struct {
	Results []commandResult
	calls   int
}

func (m *MockExecutor) Execute(command string) commandResult {
	if m.calls < len(m.Results) {
		r := m.Results[m.calls]
		m.calls++
		return r
	}
	return commandResult{ExitCode: 0, Duration: time.Millisecond}
}

func TestMockExecutor_SuccessRecordsDuration(t *testing.T) {
	mock := NewMockStateTracker()
	runner := &Runner{
		State:    mock,
		Executor: &MockExecutor{Results: []commandResult{{ExitCode: 0, Duration: 1500 * time.Millisecond}}},
	}

	err := runner.Run("test", "anything", config.RetryConfig{Attempts: new(1)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ts := mock.GetTaskState("test")
	if ts.LastDurationMs != 1500 {
		t.Errorf("expected 1500ms duration, got %d", ts.LastDurationMs)
	}
	if ts.LastExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", ts.LastExitCode)
	}
}

func TestMockExecutor_RetrySucceedsOnSecondAttempt(t *testing.T) {
	mock := NewMockStateTracker()
	exec := &MockExecutor{Results: []commandResult{
		{ExitCode: 1, Duration: 100 * time.Millisecond},
		{ExitCode: 0, Duration: 200 * time.Millisecond},
	}}
	runner := &Runner{State: mock, Executor: exec}

	err := runner.Run("test", "cmd", config.RetryConfig{Attempts: new(3), Delay: "1ms"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ts := mock.GetTaskState("test")
	if ts.LastExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", ts.LastExitCode)
	}
	if ts.ConsecutiveFailures != 0 {
		t.Errorf("expected 0 consecutive failures, got %d", ts.ConsecutiveFailures)
	}
	if exec.calls != 2 {
		t.Errorf("expected 2 executor calls, got %d", exec.calls)
	}
	if mock.SaveCalled != 2 {
		t.Errorf("expected 2 saves (one fail, one success), got %d", mock.SaveCalled)
	}
}

func TestMockExecutor_ExitCode42(t *testing.T) {
	mock := NewMockStateTracker()
	runner := &Runner{
		State:    mock,
		Executor: &MockExecutor{Results: []commandResult{{ExitCode: 42, Duration: time.Millisecond}}},
	}

	err := runner.Run("test", "cmd", config.RetryConfig{Attempts: new(1)})
	if err == nil {
		t.Fatal("expected error")
	}

	ts := mock.GetTaskState("test")
	if ts.LastExitCode != 42 {
		t.Errorf("expected exit code 42, got %d", ts.LastExitCode)
	}
}

func TestTaskRunner_NilAttempts(t *testing.T) {
	mock := NewMockStateTracker()
	runner := &Runner{State: mock, Executor: &MockExecutor{Results: []commandResult{{ExitCode: 0}}}}

	// nil Attempts → runs exactly once
	err := runner.Run("test", "echo", config.RetryConfig{Attempts: nil, Delay: ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.SaveCalled != 1 {
		t.Errorf("expected 1 save, got %d", mock.SaveCalled)
	}
}

func TestTaskRunner_ZeroDelay(t *testing.T) {
	mock := NewMockStateTracker()
	results := []commandResult{{ExitCode: 1}, {ExitCode: 1}, {ExitCode: 0}}
	runner := &Runner{State: mock, Executor: &MockExecutor{Results: results}}

	err := runner.Run("test", "echo", config.RetryConfig{Attempts: new(3), Delay: "0s"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ts := mock.GetTaskState("test")
	if ts.LastExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", ts.LastExitCode)
	}
	// Verify all 3 attempts were executed
	if mock.SaveCalled != 3 {
		t.Errorf("expected 3 saves (one per attempt), got %d", mock.SaveCalled)
	}
}
