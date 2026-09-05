package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/state"
	"go.guillerg.dev/orbit/internal/systemd"
)

func taskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Task management commands",
		Long:  `Manage orbit tasks: run, list, status, logs, skip, unskip.`,
	}

	cmd.AddCommand(taskRunCmd())
	cmd.AddCommand(taskListCmd())
	cmd.AddCommand(taskStatusCmd())
	cmd.AddCommand(taskLogsCmd())
	cmd.AddCommand(taskSkipCmd())
	cmd.AddCommand(taskUnskipCmd())

	return cmd
}

// newRunCmd builds the run command for either mount point.
func newRunCmd(short, long string) *cobra.Command {
	return &cobra.Command{
		Use:               "run NAME",
		Short:             short,
		Long:              long,
		Args:              cobra.MaximumNArgs(1),
		Aliases:           []string{"r"},
		ValidArgsFunction: completeNames(taskNames),
		RunE: func(_ *cobra.Command, args []string) error {
			return runTaskNow(args)
		},
	}
}

func taskRunCmd() *cobra.Command {
	return newRunCmd(
		"Run a task immediately",
		`Run a task immediately via systemd. Output is captured to the journal and printed after the run completes.`,
	)
}

// runTaskNow is the shared implementation behind "task run" and root "run".
func runTaskNow(args []string) error {
	stateStore, err := newState()
	if err != nil {
		return err
	}

	if err := rejectWrongKind(stateStore, args, kindTask); err != nil {
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

	// The service cannot tell a manual start from a scheduled one, so the skip
	// is resolved here: cleared on request, or the run is refused.
	if resume, skipped := skipResume(taskConfig.Schedule, stateStore.GetTaskState(name).SkipUntil); skipped {
		if !isInteractive() {
			return fmt.Errorf("'%s' is skipped (resumes %s); run 'orbit unskip %s' first", name, formatTime(resume), name)
		}
		if !confirm(fmt.Sprintf("'%s' is skipped (resumes %s). Clear the skip and run?", name, formatTime(resume))) {
			fmt.Println("Cancelled.")
			return nil
		}
		if err := clearSkip(stateStore, kindTask, name); err != nil {
			return err
		}
	}

	fmt.Printf("Running task %q\n", name)
	fmt.Printf("  command: %s\n", taskConfig.Command)
	attempts := taskConfig.Retry.Attempts
	if attempts > 0 {
		fmt.Printf("  retries: %d attempts, %s delay\n", attempts, taskConfig.Retry.Delay)
	}
	fmt.Println()

	start := time.Now()
	runErr := systemd.NewManager().RunTaskNow(name)

	printTaskRunLogs(name, start)

	if runErr != nil {
		return runErr
	}

	// The 'orbit _run' subprocess wrote fresh state to disk; reload it.
	if fresh, err := newState(); err == nil {
		ts := fresh.GetTaskState(name)
		fmt.Printf("\nTask %s completed successfully (%s)\n", name, formatDuration(ts.LastDurationMs))
	} else {
		fmt.Printf("\nTask %s completed successfully\n", name)
	}
	return nil
}

func taskListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all tasks",
		Long:    `Show all tasks with their schedule, last run, next run, and status.`,
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

			names := applied.TaskNames()
			sortNatural(names)

			primeNextRuns(applied.TaskSchedules())
			failed, _ := systemd.NewManager().FailedServices(names)

			tbl := newTable(colName, colSchedule, colLastRun, colNextRun, colStatus)
			for _, name := range names {
				taskConfig := applied.Tasks[name]
				ts := stateStore.GetTaskState(name)

				tbl.add(
					name,
					orNone(taskConfig.Schedule),
					formatTime(ts.LastRun),
					taskNextRun(taskConfig, ts),
					taskStatusString(taskConfig, ts, failed[name].Failed()),
				)
			}
			fmt.Print(tbl)
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
		Aliases:           []string{"s", "st"},
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

			return printTaskStatus(stateStore, name)
		},
	}
}

// printTaskStatus renders the detailed status view for a single task.
func printTaskStatus(stateStore *state.State, name string) error {
	taskConfig, ok := stateStore.GetAppliedTask(name)
	if !ok {
		return notAppliedErr(kindTask, name)
	}

	ts := stateStore.GetTaskState(name)

	fmt.Printf("Task:                  %s\n", name)
	fmt.Printf("Command:               %s\n", taskConfig.Command)
	fmt.Printf("Schedule:              %s\n", orNone(taskConfig.Schedule))
	if taskConfig.Schedule != "" {
		fmt.Printf("On missed:             %s\n", taskConfig.OnMissed)
	}
	fmt.Printf("Retry attempts:        %d\n", taskConfig.Retry.Attempts)
	fmt.Printf("Retry delay:           %s\n", taskConfig.Retry.Delay)
	if taskConfig.IfFailed.Command != "" {
		fmt.Printf("If failed:             %s\n", taskConfig.IfFailed.Command)
		fmt.Printf("If failed after:       %d failed run%s\n", taskConfig.IfFailed.After, plural(taskConfig.IfFailed.After))
	}
	fmt.Println()
	fmt.Printf("Last run:              %s\n", formatTime(ts.LastRun))
	failed, _ := systemd.NewManager().FailedServices([]string{name})
	if st, ok := failed[name]; ok {
		fmt.Printf("Last exit code:        %s\n", red(fmt.Sprintf("systemd: %s (see 'orbit logs %s')", systemdResult(st), name)))
	} else {
		exitCodeStr := green("0")
		if ts.LastExitCode != 0 {
			exitCodeStr = red(fmt.Sprintf("%d", ts.LastExitCode))
		}
		fmt.Printf("Last exit code:        %s\n", exitCodeStr)
	}
	fmt.Printf("Last duration:         %s\n", formatDuration(ts.LastDurationMs))
	failuresStr := "0"
	if ts.ConsecutiveFailures > 0 {
		failuresStr = red(fmt.Sprintf("%d", ts.ConsecutiveFailures))
	}
	fmt.Printf("Consecutive failures:  %s\n", failuresStr)
	cyclesStr := "0"
	if ts.FailedCycles > 0 {
		cyclesStr = red(fmt.Sprintf("%d", ts.FailedCycles))
	}
	fmt.Printf("Failed runs:           %s\n", cyclesStr)
	fmt.Printf("Retry attempt:         %d\n", ts.RetryAttempt)

	if resume, ok := skipResume(taskConfig.Schedule, ts.SkipUntil); ok {
		fmt.Printf("Skipped:               resumes %s\n", formatTime(resume))
	}
	fmt.Printf("Next run:              %s\n", taskNextRun(taskConfig, ts))
	return nil
}

// formatTime formats a time.Time for display.
// Uses relative time by default; absolute with --exact flag.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return cellNever
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
