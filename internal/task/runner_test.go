package task

import (
	"errors"
	"slices"
	"testing"
	"time"

	"go.guillerg.dev/orbit/internal/state"
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

func (m *MockStateTracker) UpdateTaskState(name string, fn func(*state.TaskState)) error {
	ts := m.TaskStates[name]
	fn(&ts)
	m.TaskStates[name] = ts
	m.SaveCalled++
	return m.SaveError
}

// spec builds the applied config for a task with the given retry settings.
func spec(command string, attempts int, delay string) state.AppliedTaskConfig {
	return state.AppliedTaskConfig{
		Command: command,
		Retry:   state.AppliedRetryConfig{Attempts: attempts, Delay: delay},
	}
}

func TestTaskRunner_Success(t *testing.T) {
	mock := NewMockStateTracker()
	runner := NewRunner(mock)

	err := runner.Run("test-task", spec("exit 0", 3, "1ms"))
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
	err := runner.Run("test-task", spec("exit 1", 0, "1ms"))
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
	if ts.FailedCycles != 1 {
		t.Fatalf("Expected 1 failed cycle, got %d", ts.FailedCycles)
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
	err := runner.Run("test-task", spec("exit 1", 3, "1ms"))
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
	if ts.FailedCycles != 1 {
		t.Fatalf("Expected 1 failed cycle, got %d", ts.FailedCycles)
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

	err := runner.Run("test-task", spec("exit 1", 1, "1ms"))
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	ts := mock.GetTaskState("test-task")
	if ts.ConsecutiveFailures != 1 {
		t.Fatalf("Expected 1 consecutive failure, got %d", ts.ConsecutiveFailures)
	}

	// Now succeed
	err = runner.Run("test-task", spec("exit 0", 1, "1ms"))
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
	if ts.FailedCycles != 0 {
		t.Fatalf("Expected 0 failed cycles after success, got %d", ts.FailedCycles)
	}
	if ts.RetryAttempt != 0 {
		t.Fatalf("Expected retry attempt 0 after success, got %d", ts.RetryAttempt)
	}
}

func TestTaskRunner_RetryResetAfterExhaustion(t *testing.T) {
	mock := NewMockStateTracker()
	runner := NewRunner(mock)

	// Exhaust 3 retries
	err := runner.Run("test-task", spec("exit 1", 3, "1ms"))
	if err == nil {
		t.Fatal("Expected error")
	}

	ts := mock.GetTaskState("test-task")
	if ts.RetryAttempt != 3 {
		t.Fatalf("Expected retry attempt 3, got %d", ts.RetryAttempt)
	}

	// Next invocation should reset retry attempt but preserve consecutive failures
	mock.SaveCalled = 0
	err = runner.Run("test-task", spec("exit 1", 3, "1ms"))
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
	if ts.FailedCycles != 2 {
		t.Fatalf("Expected 2 failed cycles, got %d", ts.FailedCycles)
	}
	if mock.SaveCalled != 3 {
		t.Fatalf("Expected Save() called 3 times after reset, got %d", mock.SaveCalled)
	}
}

func TestTaskRunner_CommandNotFound(t *testing.T) {
	mock := NewMockStateTracker()
	runner := NewRunner(mock)

	err := runner.Run("test-task", spec("this-command-does-not-exist-12345", 1, "1ms"))
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

	err := runner.Run("test-task", spec("", 1, ""))
	if err == nil {
		t.Fatal("Expected error for empty command, got nil")
	}
}

func TestTaskRunner_SaveError(t *testing.T) {
	mock := NewMockStateTracker()
	mock.SaveError = errors.New("disk full")
	runner := NewRunner(mock)

	err := runner.Run("test-task", spec("exit 0", 1, "1ms"))
	if err == nil {
		t.Fatal("Expected error when save fails, got nil")
	}
	if !errors.Is(err, mock.SaveError) {
		t.Fatalf("Expected save error to be wrapped, got: %v", err)
	}
	if _, ok := errors.AsType[*FailedError](err); ok {
		t.Fatal("A save error is orbit's failure, not the task's")
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

	err := runner.Run("test-task", spec("exit 0", 3, "not-a-duration"))
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

	err := runner.Run("test", spec("anything", 1, ""))
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

	err := runner.Run("test", spec("cmd", 3, "1ms"))
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
	if ts.FailedCycles != 0 {
		t.Errorf("expected 0 failed cycles, got %d", ts.FailedCycles)
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

	err := runner.Run("test", spec("cmd", 1, ""))
	failed, ok := errors.AsType[*FailedError](err)
	if !ok {
		t.Fatalf("expected *FailedError, got %v", err)
	}
	if failed.Task != "test" || failed.Attempts != 1 || failed.LastExitCode != 42 {
		t.Errorf("unexpected FailedError: %+v", failed)
	}

	ts := mock.GetTaskState("test")
	if ts.LastExitCode != 42 {
		t.Errorf("expected exit code 42, got %d", ts.LastExitCode)
	}
}

func TestTaskRunner_ZeroAttemptsRunsOnce(t *testing.T) {
	mock := NewMockStateTracker()
	runner := &Runner{State: mock, Executor: &MockExecutor{Results: []commandResult{{ExitCode: 0}}}}

	err := runner.Run("test", spec("echo", 0, ""))
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

	err := runner.Run("test", spec("echo", 3, "0s"))
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

// --- if_failed hook tests ---

type hookCall struct {
	Command string
	Env     []string
	// SavesAtCall is the state tracker's save count when the hook ran.
	SavesAtCall int
}

// MockHookRunner records hook invocations.
type MockHookRunner struct {
	Calls []hookCall
	Err   error
	state *MockStateTracker
}

func (m *MockHookRunner) RunHook(command string, env []string) error {
	m.Calls = append(m.Calls, hookCall{Command: command, Env: env, SavesAtCall: m.state.SaveCalled})
	return m.Err
}

// failing builds a runner whose command always exits with code.
func failing(code int, attempts int, hook state.AppliedHookConfig) (*Runner, *MockStateTracker, *MockHookRunner, state.AppliedTaskConfig) {
	mock := NewMockStateTracker()
	results := make([]commandResult, 0, attempts)
	for range max(attempts, 1) {
		results = append(results, commandResult{ExitCode: code, Duration: 250 * time.Millisecond})
	}
	hooks := &MockHookRunner{state: mock}
	runner := &Runner{State: mock, Executor: &MockExecutor{Results: results}, Hooks: hooks}
	cfg := spec("cmd", attempts, "")
	cfg.IfFailed = hook
	return runner, mock, hooks, cfg
}

func TestIfFailed_FiresOnceAfterRetriesExhausted(t *testing.T) {
	runner, mock, hooks, cfg := failing(7, 3, state.AppliedHookConfig{Command: "notify", After: 1})

	err := runner.Run("backup", cfg)
	if _, ok := errors.AsType[*FailedError](err); !ok {
		t.Fatalf("expected *FailedError, got %v", err)
	}

	if len(hooks.Calls) != 1 {
		t.Fatalf("expected hook to fire once per cycle, fired %d times", len(hooks.Calls))
	}
	call := hooks.Calls[0]
	if call.Command != "notify" {
		t.Errorf("unexpected hook command %q", call.Command)
	}
	if call.SavesAtCall != mock.SaveCalled || call.SavesAtCall != 3 {
		t.Errorf("state must be saved before the hook runs: saves at call %d, total %d", call.SavesAtCall, mock.SaveCalled)
	}
	for _, want := range []string{
		"ORBIT_TASK=backup",
		"ORBIT_EXIT_CODE=7",
		"ORBIT_ATTEMPTS=3",
		"ORBIT_CONSECUTIVE_FAILURES=3",
		"ORBIT_FAILED_CYCLES=1",
		"ORBIT_DURATION_MS=250",
	} {
		if !slices.Contains(call.Env, want) {
			t.Errorf("hook env missing %q, got %v", want, call.Env)
		}
	}
}

func TestIfFailed_NotFiredWhenRetrySucceeds(t *testing.T) {
	mock := NewMockStateTracker()
	hooks := &MockHookRunner{state: mock}
	runner := &Runner{
		State:    mock,
		Executor: &MockExecutor{Results: []commandResult{{ExitCode: 1}, {ExitCode: 0}}},
		Hooks:    hooks,
	}
	cfg := spec("cmd", 3, "")
	cfg.IfFailed = state.AppliedHookConfig{Command: "notify", After: 1}

	if err := runner.Run("backup", cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hooks.Calls) != 0 {
		t.Errorf("hook must not fire when a retry succeeds, fired %d times", len(hooks.Calls))
	}
}

func TestIfFailed_NoCommandNoHook(t *testing.T) {
	runner, _, hooks, cfg := failing(1, 2, state.AppliedHookConfig{})

	if err := runner.Run("backup", cfg); err == nil {
		t.Fatal("expected error")
	}
	if len(hooks.Calls) != 0 {
		t.Errorf("hook fired without a command configured")
	}
}

func TestIfFailed_AfterThreshold(t *testing.T) {
	hook := state.AppliedHookConfig{Command: "notify", After: 2}
	mock := NewMockStateTracker()
	hooks := &MockHookRunner{state: mock}
	cfg := spec("cmd", 2, "")
	cfg.IfFailed = hook

	cycle := func() {
		runner := &Runner{
			State:    mock,
			Executor: &MockExecutor{Results: []commandResult{{ExitCode: 1}, {ExitCode: 1}}},
			Hooks:    hooks,
		}
		if err := runner.Run("backup", cfg); err == nil {
			t.Fatal("expected error")
		}
	}

	cycle()
	if len(hooks.Calls) != 0 {
		t.Fatalf("hook fired after 1 failed cycle with after=2")
	}
	cycle()
	if len(hooks.Calls) != 1 {
		t.Fatalf("hook should fire on the 2nd failed cycle, fired %d times", len(hooks.Calls))
	}
	if !slices.Contains(hooks.Calls[0].Env, "ORBIT_FAILED_CYCLES=2") {
		t.Errorf("expected ORBIT_FAILED_CYCLES=2, got %v", hooks.Calls[0].Env)
	}
	cycle()
	if len(hooks.Calls) != 2 {
		t.Fatalf("hook should keep firing once the threshold is reached, fired %d times", len(hooks.Calls))
	}
}

func TestIfFailed_HookErrorDoesNotMaskTaskFailure(t *testing.T) {
	runner, mock, hooks, cfg := failing(3, 1, state.AppliedHookConfig{Command: "notify", After: 1})
	hooks.Err = errors.New("hook exploded")

	err := runner.Run("backup", cfg)
	failed, ok := errors.AsType[*FailedError](err)
	if !ok {
		t.Fatalf("expected *FailedError, got %v", err)
	}
	if failed.LastExitCode != 3 {
		t.Errorf("expected the task's exit code 3, got %d", failed.LastExitCode)
	}
	if errors.Is(err, hooks.Err) {
		t.Error("hook error leaked into the task result")
	}
	if ts := mock.GetTaskState("backup"); ts.LastExitCode != 3 {
		t.Errorf("state exit code changed by the hook: %d", ts.LastExitCode)
	}
}
