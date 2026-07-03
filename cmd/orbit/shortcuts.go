package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// This file defines top-level shortcuts that make the "task"/"reminder"
// namespace optional for the common verbs. Each resolves NAME to its kind and
// delegates to the shared implementation used by the namespaced commands.

// rootRunCmd is the top-level "orbit run" shortcut for "orbit task run".
func rootRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "run NAME",
		Short:             "Run a task immediately (shortcut for 'task run')",
		Args:              cobra.MaximumNArgs(1),
		Aliases:           []string{"r"},
		ValidArgsFunction: completeNames(taskNames),
		RunE:              taskRunRunE,
	}
}

// rootStatusCmd is the top-level "orbit status" shortcut that shows the detailed
// view of a task or reminder, resolving the kind from NAME. If a name is used by
// both a task and a reminder, both views are shown.
func rootStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status [NAME]",
		Short:   "Detailed view of a task or reminder",
		Args:    cobra.MaximumNArgs(1),
		Aliases: []string{"s", "st"},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return allEntryNames(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
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
				names := applied.Names()
				if len(names) == 0 {
					return fmt.Errorf("nothing configured (run 'orbit apply' first)")
				}
				sortNatural(names)
				if name, err = pickFromList(names, "Select task or reminder:"); err != nil {
					return err
				}
			}

			isTask, isReminder := applied.Classify(name)
			switch {
			case isTask && isReminder:
				if err := printTaskStatus(stateStore, name); err != nil {
					return err
				}
				fmt.Println()
				fmt.Println(dim("——————————"))
				fmt.Println()
				return printReminderStatus(stateStore, name)
			case isTask:
				return printTaskStatus(stateStore, name)
			case isReminder:
				return printReminderStatus(stateStore, name)
			default:
				return notAppliedErr("entry", name)
			}
		},
	}
}
