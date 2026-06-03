package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/picker"
	"go.guillerg.dev/orbit/internal/state"
	"go.guillerg.dev/orbit/internal/systemd"
)

func enableCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "enable [NAME]",
		Short: "Enable a disabled task or reminder",
		Long:  `Re-enable a previously disabled task or reminder, starting its timer.`,
		Args:  cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return allEntryNames(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnableDisable(args, all, false)
		},
	}

	cmd.Flags().BoolVarP(&all, "all", "a", false, "Enable all tasks and reminders")

	return cmd
}

func disableCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "disable [NAME]",
		Short: "Disable a task or reminder",
		Long:  `Disable a task or reminder, stopping its timer. It remains in the config but won't fire until re-enabled.`,
		Args:  cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return allEntryNames(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnableDisable(args, all, true)
		},
	}

	cmd.Flags().BoolVarP(&all, "all", "a", false, "Disable all tasks and reminders")

	return cmd
}

func runEnableDisable(args []string, all bool, disable bool) error {
	stateStore, err := newState()
	if err != nil {
		return err
	}

	applied := stateStore.GetAppliedConfig()
	if applied == nil {
		return fmt.Errorf("no applied config (run 'orbit apply' first)")
	}

	action := "enabled"
	if disable {
		action = "disabled"
	}

	var names []string
	if all {
		for name := range applied.Tasks {
			names = append(names, name)
		}
		for name := range applied.Reminders {
			names = append(names, name)
		}
		sortNatural(names)
	} else if len(args) > 0 {
		name := args[0]
		if !entryExists(applied, name) {
			return notAppliedErr("entry", name)
		}
		names = []string{name}
	} else {
		// filter picker — only show entries that would change
		var candidates []string
		for name := range applied.Tasks {
			if stateStore.GetTaskState(name).Disabled == disable {
				continue
			}
			candidates = append(candidates, name)
		}
		for name := range applied.Reminders {
			if stateStore.GetReminderState(name).Disabled == disable {
				continue
			}
			candidates = append(candidates, name)
		}
		if len(candidates) == 0 {
			if disable {
				return fmt.Errorf("no entries to disable (all already disabled)")
			}
			return fmt.Errorf("no entries to enable (none are disabled)")
		}
		sortNatural(candidates)
		name, pickErr := pickFromList(candidates, fmt.Sprintf("Select entry to %s:", strings.TrimSuffix(action, "d")))
		if pickErr != nil {
			return pickErr
		}
		names = []string{name}
	}

	manager := systemd.NewManager(resolveOrbitBinary())
	var changed int
	var toStop, toStart []string

	for _, name := range names {
		if _, ok := applied.Tasks[name]; ok {
			ts := stateStore.GetTaskState(name)
			if ts.Disabled != disable {
				ts.Disabled = disable
				stateStore.SetTaskState(name, ts)
				changed++

				timerName := systemd.TaskTimerName(name)
				if disable {
					toStop = append(toStop, timerName)
				} else {
					cfg := applied.Tasks[name]
					if cfg.Schedule != "" {
						toStart = append(toStart, timerName)
					}
				}
			}
		}
		if _, ok := applied.Reminders[name]; ok {
			rs := stateStore.GetReminderState(name)
			if rs.Disabled != disable {
				rs.Disabled = disable
				stateStore.SetReminderState(name, rs)
				changed++

				timerName := systemd.ReminderTimerName(name)
				if disable {
					toStop = append(toStop, timerName)
				} else {
					toStart = append(toStart, timerName)
				}
			}
		}
	}

	manager.StopAndDisableTimers(toStop)
	manager.EnableAndStartTimers(toStart)

	if err := stateStore.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	colorAction := green(action)
	if disable {
		colorAction = yellow(action)
	}

	if changed == 0 {
		if all {
			fmt.Printf("All entries already %s.\n", action)
		} else {
			fmt.Printf("'%s' already %s.\n", bold(names[0]), action)
		}
		return nil
	}

	if all {
		fmt.Printf("%d entries %s.\n", changed, colorAction)
	} else if changed > 1 {
		// Name existed in both tasks and reminders
		fmt.Printf("'%s' %s %s.\n", bold(names[0]), colorAction, dim("(task + reminder)"))
	} else {
		fmt.Printf("'%s' %s.\n", bold(names[0]), colorAction)
	}
	return nil
}

func entryExists(applied *state.AppliedConfig, name string) bool {
	if _, ok := applied.Tasks[name]; ok {
		return true
	}
	_, ok := applied.Reminders[name]
	return ok
}

func allNamesFromApplied(applied *state.AppliedConfig) []string {
	var names []string
	for name := range applied.Tasks {
		names = append(names, name)
	}
	for name := range applied.Reminders {
		names = append(names, name)
	}
	return names
}

func allEntryNames() []string {
	stateStore, err := newState()
	if err != nil {
		return nil
	}
	applied := stateStore.GetAppliedConfig()
	if applied == nil {
		return nil
	}
	names := allNamesFromApplied(applied)
	sortNatural(names)
	return names
}

func pickFromList(names []string, prompt string) (string, error) {
	if len(names) == 0 {
		return "", fmt.Errorf("no entries available")
	}
	return picker.Pick(prompt, names)
}
