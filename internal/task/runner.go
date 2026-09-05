package task

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"go.guillerg.dev/orbit/internal/state"
)

// ExitTaskFailed is the exit code of `orbit _run` when the task command
// failed after all retries. Exit 1 means orbit itself failed.
const ExitTaskFailed = 10

// FailedError reports a task command that failed after all retries.
type FailedError struct {
	Task         string
	Attempts     int
	LastExitCode int
}

func (e *FailedError) Error() string {
	return fmt.Sprintf("'%s' failed after %d attempts (last exit code: %d)", e.Task, e.Attempts, e.LastExitCode)
}

// StateTracker defines the interface for state operations needed by Runner.
// Writes go through UpdateTaskState so a run that takes hours cannot overwrite
// what the user changed on the entry in the meantime.
type StateTracker interface {
	GetTaskState(name string) state.TaskState
	UpdateTaskState(name string, fn func(*state.TaskState)) error
}

// commandResult holds the outcome of running a command.
type commandResult struct {
	ExitCode int
	Duration time.Duration
}

// Executor runs shell commands. The default implementation uses exec.Command.
type Executor interface {
	Execute(command string) commandResult
}

// HookRunner runs hook commands with extra environment variables.
type HookRunner interface {
	RunHook(command string, env []string) error
}

// shellExecutor runs commands via sh -c, with stdout/stderr passed through.
type shellExecutor struct{}

func (shellExecutor) Execute(command string) commandResult {
	start := time.Now()
	err := shellCommand(command, nil).Run()
	dur := time.Since(start)

	if err != nil {
		exitCode := 1
		if exitError, ok := errors.AsType[*exec.ExitError](err); ok {
			exitCode = exitError.ExitCode()
		}
		return commandResult{ExitCode: exitCode, Duration: dur}
	}
	return commandResult{ExitCode: 0, Duration: dur}
}

func (shellExecutor) RunHook(command string, env []string) error {
	return shellCommand(command, env).Run()
}

func shellCommand(command string, env []string) *exec.Cmd {
	cmd := exec.Command("sh", "-c", command)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// Runner handles task execution with retry logic.
type Runner struct {
	State    StateTracker
	Executor Executor
	Hooks    HookRunner
}

// NewRunner creates a new task runner with the default shell executor.
func NewRunner(s StateTracker) *Runner {
	return &Runner{State: s, Executor: shellExecutor{}, Hooks: shellExecutor{}}
}

// Run executes a task command with retry logic. Retries are handled
// internally: the command is re-run up to cfg.Retry.Attempts times with
// cfg.Retry.Delay between attempts. When every attempt fails, Run returns a
// *FailedError after firing the if_failed hook.
func (r *Runner) Run(name string, cfg state.AppliedTaskConfig) error {
	if cfg.Command == "" {
		return fmt.Errorf("empty command")
	}

	taskState := r.State.GetTaskState(name)

	// If we previously exhausted retries, reset attempt counter for a fresh cycle.
	// ConsecutiveFailures is NOT reset here — it only resets on success.
	maxAttempts := max(cfg.Retry.Attempts, 1) // always run at least once
	if taskState.RetryAttempt >= maxAttempts {
		taskState.RetryAttempt = 0
	}

	delay, err := parseDelay(cfg.Retry.Delay)
	if err != nil {
		return fmt.Errorf("invalid retry delay %q: %w", cfg.Retry.Delay, err)
	}

	startAttempt := taskState.RetryAttempt

	for attempt := startAttempt; attempt < maxAttempts; attempt++ {
		// Sleep before retry (skip delay on the very first attempt)
		if attempt > startAttempt && delay > 0 {
			time.Sleep(delay)
		}

		result := r.Executor.Execute(cfg.Command)

		taskState.LastRun = time.Now()
		taskState.LastDurationMs = result.Duration.Milliseconds()
		taskState.LastExitCode = result.ExitCode

		if result.ExitCode == 0 {
			taskState.ConsecutiveFailures = 0
			taskState.FailedCycles = 0
			taskState.RetryAttempt = 0
			if saveErr := r.recordRun(name, taskState); saveErr != nil {
				return fmt.Errorf("failed to save state after task success: %w", saveErr)
			}
			return nil
		}

		// Failure
		taskState.ConsecutiveFailures++
		taskState.RetryAttempt++
		if attempt+1 == maxAttempts {
			taskState.FailedCycles++
		}
		fmt.Fprintf(os.Stderr, "[ORBIT] Command `%s` failed (exit: %d) (attempt: %d)\n", cfg.Command, result.ExitCode, taskState.RetryAttempt)

		if saveErr := r.recordRun(name, taskState); saveErr != nil {
			return fmt.Errorf("failed to save state after task failure: %w", saveErr)
		}
	}

	// State is already on disk, so a hung hook cannot lose the failure record.
	r.runIfFailed(name, cfg.IfFailed, taskState)

	return &FailedError{Task: name, Attempts: taskState.RetryAttempt, LastExitCode: taskState.LastExitCode}
}

// recordRun persists the run-tracking fields of run, which the runner owns
// exclusively, without touching the fields user commands own.
func (r *Runner) recordRun(name string, run state.TaskState) error {
	return r.State.UpdateTaskState(name, func(ts *state.TaskState) {
		ts.LastRun = run.LastRun
		ts.LastDurationMs = run.LastDurationMs
		ts.LastExitCode = run.LastExitCode
		ts.ConsecutiveFailures = run.ConsecutiveFailures
		ts.FailedCycles = run.FailedCycles
		ts.RetryAttempt = run.RetryAttempt
	})
}

// runIfFailed fires the if_failed hook once the failed-cycle threshold is
// reached. A failing hook only warns: it must not mask the task failure.
func (r *Runner) runIfFailed(name string, hook state.AppliedHookConfig, ts state.TaskState) {
	if hook.Command == "" || ts.FailedCycles < hook.After {
		return
	}

	env := []string{
		"ORBIT_TASK=" + name,
		fmt.Sprintf("ORBIT_EXIT_CODE=%d", ts.LastExitCode),
		fmt.Sprintf("ORBIT_ATTEMPTS=%d", ts.RetryAttempt),
		fmt.Sprintf("ORBIT_CONSECUTIVE_FAILURES=%d", ts.ConsecutiveFailures),
		fmt.Sprintf("ORBIT_FAILED_CYCLES=%d", ts.FailedCycles),
		fmt.Sprintf("ORBIT_DURATION_MS=%d", ts.LastDurationMs),
	}
	if err := r.Hooks.RunHook(hook.Command, env); err != nil {
		fmt.Fprintf(os.Stderr, "[ORBIT] Warning: if_failed hook failed: %v\n", err)
	}
}

// parseDelay parses a delay string like "5m" or "30s" into a time.Duration.
// Returns 0 for an empty string.
func parseDelay(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}
