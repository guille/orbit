package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/guille/orbit/internal/systemd"
	"github.com/guille/orbit/internal/task"
)

const defaultLogLines = 50

func taskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "task",
		Short:   "Task management commands",
		Long:    `Manage orbit tasks: run, list, status, logs.`,
		Aliases: []string{"t"},
	}

	cmd.AddCommand(taskRunCmd())
	cmd.AddCommand(taskListCmd())
	cmd.AddCommand(taskStatusCmd())
	cmd.AddCommand(taskLogsCmd())

	return cmd
}

func taskRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "run NAME",
		Short:             "Run a task immediately",
		Long:              `Run a task immediately, executing the command directly with output streamed to the terminal.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeNames(taskNames),
		RunE: func(cmd *cobra.Command, args []string) error {
			stateStore, err := newState()
			if err != nil {
				return err
			}

			name, err := pickName(args, "Select task to run:", stateStore, kindTask, taskNames)
			if err != nil {
				return err
			}
			taskConfig, ok := stateStore.GetAppliedTask(name)
			if !ok {
				return notAppliedErr(kindTask, name)
			}

			// Run directly using the same code path as orbit _run
			fmt.Printf("Running task %q\n", name)
			fmt.Printf("  command: %s\n", taskConfig.Command)
			attempts := taskConfig.Retry.Attempts
			if attempts > 0 {
				fmt.Printf("  retries: %d attempts, %s delay\n", attempts, taskConfig.Retry.Delay)
			}
			fmt.Println()

			runner := task.NewRunner(stateStore)

			if err := runner.Run(name, taskConfig.Command, taskConfig.Retry.ToRetryConfig()); err != nil {
				return err
			}

			ts := stateStore.GetTaskState(name)
			fmt.Printf("Task %s completed successfully (%s)\n", name, formatDuration(ts.LastDurationMs))
			return nil
		},
	}

	return cmd
}

func taskListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all tasks",
		Long:    `Show all tasks with their last run, next run, and status.`,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			stateStore, err := newState()
			if err != nil {
				return err
			}

			applied := stateStore.GetAppliedConfig()
			if applied == nil || len(applied.Tasks) == 0 {
				fmt.Println("No tasks configured (run 'orbit apply' first)")
				return nil
			}

			fmt.Printf("%-20s %-20s %-30s %-15s\n", "TASK", "LAST RUN", "NEXT RUN", "STATUS")
			fmt.Printf("%-20s %-20s %-30s %-15s\n", "----", "--------", "--------", "------")

			names := make([]string, 0, len(applied.Tasks))
			for name := range applied.Tasks {
				names = append(names, name)
			}
			sortNatural(names)

			for _, name := range names {
				taskConfig := applied.Tasks[name]
				ts := stateStore.GetTaskState(name)

				lastRunStr := "never"
				if !ts.LastRun.IsZero() {
					lastRunStr = formatTime(ts.LastRun)
				}

				nextRunStr := "(manual)"
				if taskConfig.Schedule != "" {
					nextRunStr = resolveNextRun(taskConfig.Schedule)
				}

				fmt.Printf("%-20s %-20s %-30s %-15s\n", name, lastRunStr, nextRunStr, taskStatusString(ts))
			}
			return nil
		},
	}
}

func taskStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "status NAME",
		Short:             "Detailed view of one task",
		Long:              `Show detailed information about a specific task.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeNames(taskNames),
		RunE: func(cmd *cobra.Command, args []string) error {
			stateStore, err := newState()
			if err != nil {
				return err
			}

			name, err := pickName(args, "Select task:", stateStore, kindTask, taskNames)
			if err != nil {
				return err
			}

			taskConfig, ok := stateStore.GetAppliedTask(name)
			if !ok {
				return notAppliedErr(kindTask, name)
			}

			ts := stateStore.GetTaskState(name)

			fmt.Printf("Task:                  %s\n", name)
			fmt.Printf("Command:               %s\n", taskConfig.Command)
			scheduleDisplay := "(manual)"
			if taskConfig.Schedule != "" {
				scheduleDisplay = taskConfig.Schedule
			}
			fmt.Printf("Schedule:              %s\n", scheduleDisplay)
			if taskConfig.Schedule != "" {
				fmt.Printf("On missed:             %s\n", taskConfig.OnMissed)
			}
			fmt.Printf("Retry attempts:        %d\n", taskConfig.Retry.Attempts)
			fmt.Printf("Retry delay:           %s\n", taskConfig.Retry.Delay)
			fmt.Println()
			fmt.Printf("Last run:              %s\n", formatTime(ts.LastRun))
			exitCodeStr := green("0")
			if ts.LastExitCode != 0 {
				exitCodeStr = red(fmt.Sprintf("%d", ts.LastExitCode))
			}
			fmt.Printf("Last exit code:        %s\n", exitCodeStr)
			fmt.Printf("Last duration:         %s\n", formatDuration(ts.LastDurationMs))
			failuresStr := "0"
			if ts.ConsecutiveFailures > 0 {
				failuresStr = red(fmt.Sprintf("%d", ts.ConsecutiveFailures))
			}
			fmt.Printf("Consecutive failures:  %s\n", failuresStr)
			fmt.Printf("Retry attempt:         %d\n", ts.RetryAttempt)
			return nil
		},
	}
}

func taskLogsCmd() *cobra.Command {
	var follow bool
	var since string
	var lines int

	cmd := &cobra.Command{
		Use:               "logs NAME",
		Short:             "Show logs for a task",
		Long:              `Show journalctl logs for a task's systemd service.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeNames(taskNames),
		RunE: func(cmd *cobra.Command, args []string) error {
			stateStore, err := newState()
			if err != nil {
				return err
			}

			name, err := pickName(args, "Select task:", stateStore, kindTask, taskNames)
			if err != nil {
				return err
			}

			unitName := systemd.TaskServiceName(name)

			journalArgs := []string{"--user", "-u", unitName, "--no-pager"}

			if since != "" {
				journalArgs = append(journalArgs, "--since", since)
			} else {
				journalArgs = append(journalArgs, "-n", fmt.Sprintf("%d", lines))
			}

			if follow {
				journalArgs = append(journalArgs, "-f")
			}

			c := exec.Command("journalctl", journalArgs...)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr

			if err := c.Run(); err != nil {
				return fmt.Errorf("fetching logs: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().StringVarP(&since, "since", "S", "", "Show logs since timestamp (e.g. '1 hour ago', '2024-01-01')")
	cmd.Flags().IntVarP(&lines, "lines", "n", defaultLogLines, "Number of log lines to show")

	return cmd
}

// formatTime formats a time.Time for display.
// Uses relative time by default; absolute with --exact flag.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	if exactTime {
		return t.Format("2006-01-02 15:04:05")
	}
	return relativeTime(t)
}

// relativeTime returns a human-friendly relative time string.
func relativeTime(t time.Time) string {
	d := time.Since(t)
	future := d < 0
	if future {
		d = -d
	}

	var s string
	switch {
	case d < time.Minute:
		s = "<1m"
	case d < time.Hour:
		s = fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m > 0 {
			s = fmt.Sprintf("%dh %dm", h, m)
		} else {
			s = fmt.Sprintf("%dh", h)
		}
	case d < 30*24*time.Hour:
		s = fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		weeks := int(d.Hours() / 24 / 7)
		s = fmt.Sprintf("%dw", weeks)
	}

	if future {
		return "in " + s
	}
	return s + " ago"
}

// formatDuration formats milliseconds into a human-friendly string using the
// two largest applicable units: hours+minutes, minutes+seconds, or
// seconds+milliseconds.
func formatDuration(ms int64) string {
	if ms <= 0 {
		return "0ms"
	}

	hours := ms / 3_600_000
	ms %= 3_600_000
	minutes := ms / 60_000
	ms %= 60_000
	seconds := ms / 1000
	millis := ms % 1000

	switch {
	case hours > 0:
		if minutes > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	case minutes > 0:
		if seconds > 0 {
			return fmt.Sprintf("%dm %ds", minutes, seconds)
		}
		return fmt.Sprintf("%dm", minutes)
	case seconds > 0:
		if millis > 0 {
			return fmt.Sprintf("%ds %dms", seconds, millis)
		}
		return fmt.Sprintf("%ds", seconds)
	default:
		return fmt.Sprintf("%dms", millis)
	}
}
