package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/state"
)

// rootCmd is the root command for the orbit CLI
var rootCmd = &cobra.Command{
	Use:   "orbit",
	Short: "A declarative frontend for systemd timers",
	Long: `Orbit manages user systemd timers and services based on a TOML configuration file.
It provides task scheduling with retry logic and reminder functionality with acknowledgment and snooze capabilities.`,
	SilenceUsage: true,
	RunE:         runDashboard,
}

// runDashboard prints a summary of active tasks and pending reminders when orbit is invoked with no subcommand.
func runDashboard(cmd *cobra.Command, args []string) error {
	stateStore, err := newState()
	if err != nil {
		return err
	}

	applied := stateStore.GetAppliedConfig()
	if applied == nil {
		fmt.Println("No applied configuration.")
		fmt.Printf("Run %s to create your config, then %s to activate it.\n", bold("orbit edit"), bold("orbit apply"))
		return nil
	}

	totalTasks := len(applied.Tasks)
	failedTasks := 0
	disabledTasks := 0
	var failedNames []string
	for name := range applied.Tasks {
		ts := stateStore.GetTaskState(name)
		if ts.Disabled {
			disabledTasks++
		} else if ts.ConsecutiveFailures > 0 {
			failedTasks++
			failedNames = append(failedNames, name)
		}
	}
	sortNatural(failedNames)

	totalReminders := len(applied.Reminders)
	pendingCount := 0
	snoozedCount := 0
	disabledReminders := 0
	var pendingNames []string
	for name := range applied.Reminders {
		rs := stateStore.GetReminderState(name)
		if rs.Disabled {
			disabledReminders++
			continue
		}
		switch rs.State {
		case state.StatePending:
			pendingCount++
			pendingNames = append(pendingNames, name)
		case state.StateSnoozed:
			snoozedCount++
		}
	}
	sortNatural(pendingNames)

	taskLine := fmt.Sprintf("Tasks:     %d active", totalTasks-disabledTasks)
	if failedTasks > 0 {
		taskLine += fmt.Sprintf(", %s", red(fmt.Sprintf("%d failed", failedTasks)))
	}
	if disabledTasks > 0 {
		taskLine += fmt.Sprintf(", %s", dim(fmt.Sprintf("%d disabled", disabledTasks)))
	}
	fmt.Println(taskLine)

	reminderLine := fmt.Sprintf("Reminders: %d configured", totalReminders-disabledReminders)
	if pendingCount > 0 {
		reminderLine += fmt.Sprintf(", %s", yellow(fmt.Sprintf("%d pending", pendingCount)))
	}
	if snoozedCount > 0 {
		reminderLine += fmt.Sprintf(", %d snoozed", snoozedCount)
	}
	if disabledReminders > 0 {
		reminderLine += fmt.Sprintf(", %s", dim(fmt.Sprintf("%d disabled", disabledReminders)))
	}
	fmt.Println(reminderLine)

	if failedTasks > 0 || pendingCount > 0 {
		fmt.Println()
	}

	for _, name := range failedNames {
		ts := stateStore.GetTaskState(name)
		fmt.Printf("  %s %s  %d consecutive failures\n", red("!"), name, ts.ConsecutiveFailures)
	}

	for _, name := range pendingNames {
		rs := stateStore.GetReminderState(name)
		overdue := ""
		if rs.OverdueCount > 1 {
			overdue = fmt.Sprintf("  %d overdue", rs.OverdueCount)
		}
		fmt.Printf("  %s %s  pending%s\n", yellow("●"), name, overdue)
	}

	fmt.Println()
	fmt.Println(dim("Run 'orbit help' for available commands."))

	return nil
}

// exactTime controls whether timestamps are shown as absolute values.
var exactTime bool

func main() {
	rootCmd.PersistentFlags().BoolVar(&exactTime, "exact", false, "Show absolute timestamps instead of relative times")

	rootCmd.AddGroup(
		&cobra.Group{ID: "manage", Title: "Managing tasks & reminders:"},
		&cobra.Group{ID: "overview", Title: "Overview:"},
		&cobra.Group{ID: "config", Title: "Configuration:"},
		&cobra.Group{ID: "namespaces", Title: "Explicit namespaces (task/reminder):"},
	)

	addGroup := func(groupID string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = groupID
			rootCmd.AddCommand(c)
		}
	}

	addGroup("manage",
		rootRunCmd(),
		rootStatusCmd(),
		rootLogsCmd(),
		rootAckCmd(),
		rootSnoozeCmd(),
		enableCmd(),
		disableCmd(),
	)
	addGroup("overview", listCmd(), nextCmd(), doctorCmd())
	addGroup("config", editCmd(), applyCmd(), planCmd())
	addGroup("namespaces", taskCmd(), reminderCmd())

	rootCmd.AddCommand(versionCmd())

	rootCmd.AddCommand(runInternalCmd())
	rootCmd.AddCommand(notifyInternalCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
