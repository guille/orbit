package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/picker"
	"go.guillerg.dev/orbit/internal/reminder"
	"go.guillerg.dev/orbit/internal/systemd"
)

func enableCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:     "enable [NAME]",
		Short:   "Enable a disabled task or reminder",
		Long:    `Re-enable a previously disabled task or reminder, starting its timer.`,
		Args:    cobra.MaximumNArgs(1),
		Aliases: []string{"on"},
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
		Use:     "disable [NAME]",
		Short:   "Disable a task or reminder",
		Long:    `Disable a task or reminder, stopping its timer. It remains in the config but won't fire until re-enabled.`,
		Args:    cobra.MaximumNArgs(1),
		Aliases: []string{"off"},
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
		names = applied.Names()
		sortNatural(names)
	} else if len(args) > 0 {
		name := args[0]
		if !applied.HasTask(name) && !applied.HasReminder(name) {
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

	// Disabling a pending or snoozed reminder dismisses it; confirm first so the
	// dismissal is a deliberate acknowledgment rather than a silent one.
	if disable {
		var pending, snoozed []string
		for _, name := range names {
			if !applied.HasReminder(name) {
				continue
			}
			rs := stateStore.GetReminderState(name)
			if rs.Disabled || !reminder.IsActionable(rs) {
				continue
			}
			if reminder.IsSnoozed(rs) {
				snoozed = append(snoozed, name)
			} else {
				pending = append(pending, name)
			}
		}
		if len(pending)+len(snoozed) > 0 && isInteractive() {
			if !confirm(disableDismissPrompt(pending, snoozed)) {
				fmt.Println("Cancelled.")
				return nil
			}
		}
	}

	manager := systemd.NewManager()
	var changed int
	var toStop, toStart []string
	var toUnsnooze []string // reminder names, not unit names

	for _, name := range names {
		if applied.HasTask(name) {
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
		if applied.HasReminder(name) {
			rs := stateStore.GetReminderState(name)
			if rs.Disabled != disable {
				rs.Disabled = disable
				if disable && reminder.IsActionable(rs) {
					if reminder.IsSnoozed(rs) {
						toUnsnooze = append(toUnsnooze, name)
					}
					rs = reminder.Dismiss(rs)
				}
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

	removeSnoozeTimers(manager, toUnsnooze)
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

// disableDismissPrompt builds the confirmation message for disabling reminders
// that are currently pending or snoozed.
func disableDismissPrompt(pending, snoozed []string) string {
	switch {
	case len(pending) == 1 && len(snoozed) == 0:
		return fmt.Sprintf("'%s' is pending — disabling will dismiss it (its command won't run). Continue?", pending[0])
	case len(pending) == 0 && len(snoozed) == 1:
		return fmt.Sprintf("'%s' is snoozed — disabling will cancel the snooze. Continue?", snoozed[0])
	}
	var parts []string
	if len(pending) > 0 {
		parts = append(parts, fmt.Sprintf("dismiss %d pending", len(pending)))
	}
	if len(snoozed) > 0 {
		parts = append(parts, fmt.Sprintf("cancel snooze on %d", len(snoozed)))
	}
	return fmt.Sprintf("Disabling will %s reminder(s). Continue?", joinAnd(parts))
}

func allEntryNames() []string {
	stateStore, err := newState()
	if err != nil {
		return nil
	}
	names := stateStore.GetAppliedConfig().Names()
	sortNatural(names)
	return names
}

func pickFromList(names []string, prompt string) (string, error) {
	if len(names) == 0 {
		return "", fmt.Errorf("no entries available")
	}
	return picker.Pick(prompt, names)
}
