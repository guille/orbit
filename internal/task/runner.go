package task

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"go.guillerg.dev/orbit/internal/config"
	"go.guillerg.dev/orbit/internal/state"
)

// StateTracker defines the interface for state operations needed by Runner.
type StateTracker interface {
	GetTaskState(name string) state.TaskState
	SetTaskState(name string, state state.TaskState)
	Save() error
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

// shellExecutor runs commands via sh -c, with stdout/stderr passed through.
type shellExecutor struct{}

func (shellExecutor) Execute(command string) commandResult {
	start := time.Now()
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
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

// Runner handles task execution with retry logic.
type Runner struct {
	State    StateTracker
	Executor Executor
}

// NewRunner creates a new task runner with the default shell executor.
func NewRunner(s StateTracker) *Runner {
	return &Runner{State: s, Executor: shellExecutor{}}
}

// Run executes a task command with retry logic.
// Retries are handled internally: the command is re-run up to
// retryConfig.Attempts times with retryConfig.Delay between attempts.
func (r *Runner) Run(name, command string, retryConfig config.RetryConfig) error {
	if command == "" {
		return fmt.Errorf("empty command")
	}

	taskState := r.State.GetTaskState(name)

	// If we previously exhausted retries, reset attempt counter for a fresh cycle.
	// ConsecutiveFailures is NOT reset here — it only resets on success.
	attempts := retryConfig.GetAttempts()
	maxAttempts := attempts
	if maxAttempts <= 0 {
		maxAttempts = 1 // always run at least once
	}
	if taskState.RetryAttempt >= maxAttempts {
		taskState.RetryAttempt = 0
	}

	delay, err := parseDelay(retryConfig.Delay)
	if err != nil {
		return fmt.Errorf("invalid retry delay %q: %w", retryConfig.Delay, err)
	}

	startAttempt := taskState.RetryAttempt
	var lastExitCode int

	for attempt := startAttempt; attempt < maxAttempts; attempt++ {
		// Sleep before retry (skip delay on the very first attempt)
		if attempt > startAttempt && delay > 0 {
			time.Sleep(delay)
		}

		result := r.Executor.Execute(command)

		taskState.LastRun = time.Now()
		taskState.LastDurationMs = result.Duration.Milliseconds()
		taskState.LastExitCode = result.ExitCode

		if result.ExitCode == 0 {
			taskState.ConsecutiveFailures = 0
			taskState.RetryAttempt = 0
			r.State.SetTaskState(name, taskState)
			if saveErr := r.State.Save(); saveErr != nil {
				return fmt.Errorf("failed to save state after task success: %w", saveErr)
			}
			return nil
		}

		// Failure
		taskState.ConsecutiveFailures++
		taskState.RetryAttempt++
		lastExitCode = result.ExitCode
		fmt.Fprintf(os.Stderr, "[ORBIT] Command `%s` failed (exit: %d) (attempt: %d)\n", command, result.ExitCode, taskState.RetryAttempt)

		r.State.SetTaskState(name, taskState)
		if saveErr := r.State.Save(); saveErr != nil {
			return fmt.Errorf("failed to save state after task failure: %w", saveErr)
		}
	}

	// All attempts exhausted
	return fmt.Errorf("'%s' failed after %d attempts (last exit code: %d)", name, taskState.RetryAttempt, lastExitCode)
}

// parseDelay parses a delay string like "5m" or "30s" into a time.Duration.
// Returns 0 for an empty string.
func parseDelay(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}
