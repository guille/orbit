package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/systemd"
)

const defaultLogLines = 50

// addLogFlags registers the shared journalctl-style flags on a logs command.
func addLogFlags(cmd *cobra.Command, follow *bool, since *string, lines *int) {
	cmd.Flags().BoolVarP(follow, "follow", "f", false, "Follow log output")
	cmd.Flags().StringVarP(since, "since", "S", "", "Show logs since timestamp (e.g. '1 hour ago', '2024-01-01')")
	cmd.Flags().IntVarP(lines, "lines", "n", defaultLogLines, "Number of log lines to show")
}

// streamLogs streams a unit's journal to the terminal.
func streamLogs(unitName string, follow bool, since string, lines int) error {
	opts := systemd.LogOptions{Follow: follow, Since: since, Lines: lines}
	if err := systemd.NewManager().StreamUnitLogs(unitName, opts, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("fetching logs: %w", err)
	}
	return nil
}

// printTaskRunLogs prints a task service's journal output since the given start time.
func printTaskRunLogs(name string, start time.Time) {
	// journalctl --since granularity is one second; back up slightly just in case
	since := start.Add(-time.Second).Format("2006-01-02 15:04:05")
	fmt.Println("--- output ---")
	//nolint:errcheck // best-effort log display; the run result is authoritative
	streamLogs(systemd.TaskServiceName(name), false, since, 0)
	fmt.Println("--------------")
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
		Aliases:           []string{"log"},
		ValidArgsFunction: completeNames(taskNames),
		RunE:              taskLogsRunE(&follow, &since, &lines),
	}
	addLogFlags(cmd, &follow, &since, &lines)
	return cmd
}

func taskLogsRunE(follow *bool, since *string, lines *int) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		stateStore, err := newState()
		if err != nil {
			return err
		}
		if err := rejectWrongKind(stateStore, args, kindTask); err != nil {
			return err
		}
		name, err := pickName(args, "Select task:", stateStore, kindTask, taskNames)
		if err != nil {
			return err
		}
		return streamLogs(systemd.TaskServiceName(name), *follow, *since, *lines)
	}
}

func reminderLogsCmd() *cobra.Command {
	var follow bool
	var since string
	var lines int

	cmd := &cobra.Command{
		Use:               "logs NAME",
		Short:             "Show logs for a reminder",
		Long:              `Show journalctl logs for a reminder's systemd service.`,
		Args:              cobra.MaximumNArgs(1),
		Aliases:           []string{"log"},
		ValidArgsFunction: completeNames(reminderNames),
		RunE:              reminderLogsRunE(&follow, &since, &lines),
	}
	addLogFlags(cmd, &follow, &since, &lines)
	return cmd
}

func reminderLogsRunE(follow *bool, since *string, lines *int) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		stateStore, err := newState()
		if err != nil {
			return err
		}
		if err := rejectWrongKind(stateStore, args, kindReminder); err != nil {
			return err
		}
		name, err := pickName(args, "Select reminder:", stateStore, kindReminder, reminderNames)
		if err != nil {
			return err
		}
		return streamLogs(systemd.ReminderServiceName(name), *follow, *since, *lines)
	}
}

// rootLogsCmd is the top-level "orbit logs" shortcut, resolving NAME to a task or reminder.
func rootLogsCmd() *cobra.Command {
	var follow bool
	var since string
	var lines int

	cmd := &cobra.Command{
		Use:     "logs NAME",
		Short:   "Show logs for a task or reminder",
		Args:    cobra.MaximumNArgs(1),
		Aliases: []string{"log"},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return allEntryNames(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: rootLogsRunE(&follow, &since, &lines),
	}
	addLogFlags(cmd, &follow, &since, &lines)
	return cmd
}

func rootLogsRunE(follow *bool, since *string, lines *int) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		stateStore, err := newState()
		if err != nil {
			return err
		}
		applied := stateStore.GetAppliedConfig()
		if applied == nil {
			return fmt.Errorf("no applied config (run 'orbit apply' first)")
		}

		name := ""
		if len(args) > 0 {
			name = args[0]
		} else {
			names := allNamesFromApplied(applied)
			if len(names) == 0 {
				return fmt.Errorf("nothing configured (run 'orbit apply' first)")
			}
			sortNatural(names)
			if name, err = pickFromList(names, "Select task or reminder:"); err != nil {
				return err
			}
		}

		isTask, isReminder := classifyEntry(applied, name)
		switch {
		case isTask && isReminder:
			return fmt.Errorf("'%s' is both a task and a reminder — use 'orbit task logs %s' or 'orbit reminder logs %s'", name, name, name)
		case isTask:
			return streamLogs(systemd.TaskServiceName(name), *follow, *since, *lines)
		case isReminder:
			return streamLogs(systemd.ReminderServiceName(name), *follow, *since, *lines)
		default:
			return notAppliedErr("entry", name)
		}
	}
}
