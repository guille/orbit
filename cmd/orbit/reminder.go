package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"go.guillerg.dev/orbit/internal/reminder"
	"go.guillerg.dev/orbit/internal/state"
	"go.guillerg.dev/orbit/internal/systemd"
)

// colorizeReminderState applies color to a reminder state string for display.
func colorizeReminderState(s state.ReminderStatus) string {
	switch s {
	case state.StatePending, state.StateSnoozed:
		return yellow(s.String())
	case state.StateAcknowledged:
		return green(s.String())
	default:
		return dim(s.String())
	}
}

func firedAtDisplay(rs state.ReminderState) string {
	if rs.FiredAt.IsZero() {
		return "never"
	}
	return formatTime(rs.FiredAt)
}

func snoozeDisplay(rs state.ReminderState) string {
	if rs.SnoozedUntil == nil {
		return "-"
	}
	return formatTime(*rs.SnoozedUntil)
}

func reminderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "reminder",
		Short:   "Reminder management commands",
		Long:    `Manage orbit reminders: list, status, ack, snooze.`,
		Aliases: []string{"r"},
	}

	cmd.AddCommand(reminderListCmd())
	cmd.AddCommand(reminderStatusCmd())
	cmd.AddCommand(reminderAckCmd())
	cmd.AddCommand(reminderSnoozeCmd())

	return cmd
}

func reminderListCmd() *cobra.Command {
	var showAll bool

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List reminders",
		Long:    `List pending reminders. Use --all to see all reminders.`,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			stateStore, err := newState()
			if err != nil {
				return err
			}

			applied := stateStore.GetAppliedConfig()
			if applied == nil || len(applied.Reminders) == 0 {
				fmt.Println("No reminders configured (run 'orbit apply' first)")
				return nil
			}

			if showAll {
				printAllReminders(applied, stateStore)
			} else {
				printPendingReminders(applied, stateStore)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "Show all reminders, not just pending")

	return cmd
}

// printAllReminders prints a table of all configured reminders with their state.
func printAllReminders(applied *state.AppliedConfig, stateStore *state.State) {
	fmt.Printf("%-25s %-15s %-20s %-20s\n", "REMINDER", "STATE", "FIRED AT", "SNOOZE UNTIL")
	fmt.Printf("%-25s %-15s %-20s %-20s\n", "--------", "-----", "--------", "------------")

	names := make([]string, 0, len(applied.Reminders))
	for name := range applied.Reminders {
		names = append(names, name)
	}
	sortNatural(names)

	for _, name := range names {
		rs := stateStore.GetReminderState(name)

		coloredState := colorizeReminderState(rs.State)

		fmt.Printf("%-25s %s %-20s %-20s\n", name, padRight(coloredState, 15), firedAtDisplay(rs), snoozeDisplay(rs))
	}
}

// printPendingReminders prints a table of only pending and snoozed reminders.
func printPendingReminders(applied *state.AppliedConfig, stateStore *state.State) {
	var active []string
	for name := range applied.Reminders {
		rs := stateStore.GetReminderState(name)
		if rs.State == state.StatePending || rs.State == state.StateSnoozed {
			active = append(active, name)
		}
	}
	sortNatural(active)

	if len(active) == 0 {
		fmt.Println("No pending reminders")
		return
	}

	fmt.Printf("%-25s %-15s %-10s %-20s %-20s\n", "REMINDER", "STATE", "OVERDUE", "FIRED AT", "SNOOZE UNTIL")
	fmt.Printf("%-25s %-15s %-10s %-20s %-20s\n", "--------", "-----", "-------", "--------", "------------")

	for _, name := range active {
		rs := stateStore.GetReminderState(name)

		fmt.Printf("%-25s %s %-10d %-20s %-20s\n",
			name,
			padRight(colorizeReminderState(rs.State), 15),
			rs.OverdueCount,
			firedAtDisplay(rs),
			snoozeDisplay(rs),
		)
	}
}

func reminderStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "status NAME",
		Short:             "Detailed view of one reminder",
		Long:              `Show detailed information about a specific reminder.`,
		Args:              cobra.MaximumNArgs(1),
		Aliases:           []string{"st"},
		ValidArgsFunction: completeNames(reminderNames),
		RunE: func(cmd *cobra.Command, args []string) error {
			stateStore, err := newState()
			if err != nil {
				return err
			}

			name, err := pickName(args, "Select reminder:", stateStore, kindReminder, reminderNames)
			if err != nil {
				return err
			}

			reminderConfig, ok := stateStore.GetAppliedReminder(name)
			if !ok {
				return notAppliedErr(kindReminder, name)
			}

			rs := stateStore.GetReminderState(name)

			fmt.Printf("Reminder:       %s\n", name)
			fmt.Printf("Message:        %s\n", reminderConfig.Message)
			if reminderConfig.Command != "" {
				fmt.Printf("Command:        %s\n", reminderConfig.Command)
			} else {
				fmt.Printf("Command:        (none)\n")
			}
			fmt.Printf("Schedule:       %s\n", reminderConfig.Schedule)
			if reminderConfig.Check != "" {
				fmt.Printf("Check:          %s\n", reminderConfig.Check)
			}
			fmt.Printf("Snooze default: %s\n", reminderConfig.Snooze)
			fmt.Println()

			coloredState := colorizeReminderState(rs.State)
			fmt.Printf("State:          %s\n", coloredState)
			fmt.Printf("Fired at:       %s\n", formatTime(rs.FiredAt))
			if rs.SnoozedUntil != nil {
				fmt.Printf("Fires:          %s\n", formatTime(*rs.SnoozedUntil))
			}
			fmt.Printf("Overdue count:  %d\n", rs.OverdueCount)
			if reminderConfig.Check != "" && rs.LastCheckExitCode != nil {
				exitStr := fmt.Sprintf("%d", *rs.LastCheckExitCode)
				if *rs.LastCheckExitCode == 0 {
					exitStr = green(exitStr) + " (condition met)"
				} else {
					exitStr = dim(exitStr) + " (condition not met)"
				}
				fmt.Printf("Last check:     %s at %s\n", exitStr, formatTime(rs.LastCheckAt))
			}
			return nil
		},
	}
}

// ackRunE is the shared implementation for the ack command (used by both "reminder ack" and root "ack").
func ackRunE(cmd *cobra.Command, args []string) error {
	autoRun, _ := cmd.Flags().GetBool("run")

	stateStore, err := newState()
	if err != nil {
		return err
	}

	var extract nameExtractor = reminderNames
	if len(args) == 0 {
		extract = func(applied *state.AppliedConfig) []string {
			return actionableReminderNames(applied, stateStore)
		}
	}
	name, err := pickName(args, "Select reminder to acknowledge:", stateStore, kindReminder, extract)
	if err != nil {
		// Better error when reminders exist but none are actionable
		if len(args) == 0 && len(reminderNames(stateStore.GetAppliedConfig())) > 0 {
			return fmt.Errorf("no reminders pending to acknowledge")
		}
		return err
	}

	reminderConfig, ok := stateStore.GetAppliedReminder(name)
	if !ok {
		return notAppliedErr(kindReminder, name)
	}

	h := reminder.NewHandler(stateStore)
	acked, err := h.Ack(name)
	if err != nil {
		return fmt.Errorf("saving state: %w", err)
	}
	if !acked {
		rs := stateStore.GetReminderState(name)
		fmt.Printf("Reminder '%s' is not pending (current state: %s)\n", name, yellow(rs.State.String()))
		return nil
	}

	manager := systemd.NewManager()
	removeSnoozeTimer(manager, name)

	if reminderConfig.Command != "" {
		shouldRun := autoRun
		if !shouldRun && term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Printf("Reminder has a command: %s\n", reminderConfig.Command)
			fmt.Print("Run it? [Y/n] ")
			var answer string
			//nolint:errcheck
			fmt.Scanln(&answer)
			shouldRun = answer != "n" && answer != "N" && answer != "no"
		}
		if shouldRun {
			fmt.Printf("Running: %s\n", reminderConfig.Command)
			c := exec.Command("sh", "-c", reminderConfig.Command)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			if err := c.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "%s command failed: %v\n", yellow("Warning:"), err)
			}
		}
	}

	fmt.Printf("Reminder '%s' %s\n", bold(name), green("acknowledged"))
	return nil
}

func reminderAckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "ack NAME",
		Short:             "Acknowledge a reminder",
		Long:              `Mark a reminder as acknowledged, optionally running its associated command.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeNames(reminderNames),
		RunE:              ackRunE,
	}

	cmd.Flags().BoolP("run", "r", false, "Run the reminder's command without prompting")

	return cmd
}

// snoozeRunE is the shared implementation for the snooze command.
func snoozeRunE(duration *string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		stateStore, err := newState()
		if err != nil {
			return err
		}

		var extract nameExtractor = reminderNames
		if len(args) == 0 {
			extract = func(applied *state.AppliedConfig) []string {
				return actionableReminderNames(applied, stateStore)
			}
		}
		name, err := pickName(args, "Select reminder to snooze:", stateStore, kindReminder, extract)
		if err != nil {
			if len(args) == 0 && len(reminderNames(stateStore.GetAppliedConfig())) > 0 {
				return fmt.Errorf("no reminders are snoozable")
			}
			return err
		}

		reminderConfig, ok := stateStore.GetAppliedReminder(name)
		if !ok {
			return notAppliedErr(kindReminder, name)
		}

		d := *duration
		if d == "" {
			d = reminderConfig.Snooze
		}

		if d == "" {
			return fmt.Errorf("no snooze duration specified and no default configured for '%s'\n  specify one with --duration 2h, or set 'snooze' in your config", name)
		}

		dur, err := time.ParseDuration(d)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", d, err)
		}

		if dur <= 0 {
			return fmt.Errorf("snooze duration must be positive, got %s", d)
		}

		snoozeUntil := time.Now().Add(dur)
		h := reminder.NewHandler(stateStore)
		if err := h.Snooze(name, snoozeUntil); err != nil {
			return err
		}

		// Create a persistent snooze timer that triggers the reminder service.
		// This survives reboots — systemd will catch up if the time passes while off.
		manager := systemd.NewManager()

		// Remove any existing snooze timer first (for re-snooze case)
		removeSnoozeTimer(manager, name)

		snoozeUnit, err := manager.GenerateSnoozeTimer(name, snoozeUntil)
		if err != nil {
			return fmt.Errorf("generating snooze timer: %w", err)
		}
		if err := manager.ApplyUnits([]systemd.Unit{snoozeUnit}); err != nil {
			fmt.Fprintf(os.Stderr, "%s failed to create snooze timer: %v\n", yellow("Warning:"), err)
			fmt.Fprintln(os.Stderr, dim("The snooze state has been saved, but automatic re-notification may not fire."))
		}

		fmt.Printf("Reminder '%s' %s (fires %s)\n", bold(name), yellow("snoozed"), formatTime(snoozeUntil))
		return nil
	}
}

func reminderSnoozeCmd() *cobra.Command {
	var duration string

	cmd := &cobra.Command{
		Use:               "snooze [NAME]",
		Short:             "Snooze a reminder",
		Long:              `Snooze a reminder for a specified duration (e.g. 2h, 30m). If no duration is given, uses the default from config.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeNames(reminderNames),
		RunE:              snoozeRunE(&duration),
	}

	cmd.Flags().StringVarP(&duration, "duration", "d", "", "Snooze duration (e.g. 2h, 30m)")

	return cmd
}

// rootAckCmd creates a top-level "orbit ack" shortcut for "orbit reminder ack".
func rootAckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "ack NAME",
		Short:             "Acknowledge a reminder (shortcut for 'reminder ack')",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeNames(reminderNames),
		RunE:              ackRunE,
	}

	cmd.Flags().BoolP("run", "r", false, "Run the reminder's command without prompting")

	return cmd
}

// rootSnoozeCmd creates a top-level "orbit snooze" shortcut for "orbit reminder snooze".
func rootSnoozeCmd() *cobra.Command {
	var duration string

	cmd := &cobra.Command{
		Use:               "snooze [NAME]",
		Short:             "Snooze a reminder (shortcut for 'reminder snooze')",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeNames(reminderNames),
		RunE:              snoozeRunE(&duration),
	}

	cmd.Flags().StringVarP(&duration, "duration", "d", "", "Snooze duration (e.g. 2h, 30m)")

	return cmd
}

// actionableReminderNames returns reminder names that are pending or snoozed.
func actionableReminderNames(applied *state.AppliedConfig, stateStore *state.State) []string {
	if applied == nil {
		return nil
	}
	var names []string
	for name := range applied.Reminders {
		rs := stateStore.GetReminderState(name)
		if rs.State == state.StatePending || rs.State == state.StateSnoozed {
			names = append(names, name)
		}
	}
	return names
}
