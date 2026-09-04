package main

import (
	"errors"
	"fmt"
	"os"
	"time"

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

// runDashboard is the bare "orbit" home screen: it surfaces only what needs
// attention.
func runDashboard(cmd *cobra.Command, args []string) error {
	stateStore, err := newState()
	if err != nil {
		return err
	}

	applied := stateStore.GetAppliedConfig()
	if applied.IsEmpty() {
		fmt.Println("Nothing configured.")
		fmt.Printf("Run %s to create your config, then %s to activate it.\n", bold("orbit edit"), bold("orbit apply"))
		return nil
	}

	activeTasks := 0
	var failed []string
	for name := range applied.Tasks {
		ts := stateStore.GetTaskState(name)
		if ts.Disabled {
			continue
		}
		activeTasks++
		if ts.ConsecutiveFailures > 0 {
			failed = append(failed, name)
		}
	}
	sortNatural(failed)

	activeReminders := 0
	var pending []string
	for name := range applied.Reminders {
		rs := stateStore.GetReminderState(name)
		if rs.Disabled {
			continue
		}
		activeReminders++
		if rs.State == state.StatePending {
			pending = append(pending, name)
		}
	}
	sortNatural(pending)

	if len(failed) == 0 && len(pending) == 0 {
		printHealthyPulse(stateStore, applied, activeTasks, activeReminders)
		return nil
	}

	for _, name := range failed {
		ts := stateStore.GetTaskState(name)
		fmt.Printf("%s %s  %s %s\n", red("!"), name, taskStatusString(ts, applied.Tasks[name].Retry.Attempts, false), red(fmt.Sprintf("(exit %d)", ts.LastExitCode)))
		fmt.Printf("    %s\n", dim("orbit logs "+name))
	}
	for _, name := range pending {
		rs := stateStore.GetReminderState(name)
		overdue := ""
		if rs.OverdueCount > 1 {
			overdue = fmt.Sprintf(" (%d overdue)", rs.OverdueCount)
		}
		fmt.Printf("%s %s  %s%s\n", yellow("●"), name, yellow("pending"), overdue)
		fmt.Printf("    %s\n", dim("orbit ack "+name))
	}

	return nil
}

// printHealthyPulse prints the single-line all-clear summary plus the next fire.
func printHealthyPulse(stateStore *state.State, applied *state.AppliedConfig, activeTasks, activeReminders int) {
	var parts []string
	if activeTasks > 0 {
		parts = append(parts, fmt.Sprintf("%d task%s", activeTasks, plural(activeTasks)))
	}
	if activeReminders > 0 {
		parts = append(parts, fmt.Sprintf("%d reminder%s", activeReminders, plural(activeReminders)))
	}

	if len(parts) == 0 {
		fmt.Println(dim("Nothing active (all entries disabled)."))
		return
	}

	line := fmt.Sprintf("%s %s healthy", green("✓"), joinAnd(parts))
	if name, when, ok := nextUpcoming(stateStore, applied); ok {
		line += fmt.Sprintf(" · next: %s %s", name, when)
	}
	fmt.Println(line)
}

// nextUpcoming returns the soonest-firing active task or reminder and a
// human-friendly time until it fires.
func nextUpcoming(stateStore *state.State, applied *state.AppliedConfig) (name, when string, ok bool) {
	primeNextRuns(applied.Schedules())

	now := time.Now()
	var best time.Time

	consider := func(n string, t time.Time) {
		if !t.After(now) {
			return
		}
		if name == "" || t.Before(best) {
			name, best = n, t
		}
	}

	for n := range applied.Tasks {
		cfg := applied.Tasks[n]
		if cfg.Schedule == "" || stateStore.GetTaskState(n).Disabled {
			continue
		}
		if t, resolved := nextRun(cfg.Schedule); resolved {
			consider(n, t)
		}
	}
	for n := range applied.Reminders {
		rs := stateStore.GetReminderState(n)
		if rs.Disabled {
			continue
		}
		if rs.State == state.StateSnoozed && rs.SnoozedUntil != nil {
			consider(n, *rs.SnoozedUntil)
			continue
		}
		if t, resolved := nextRun(applied.Reminders[n].Schedule); resolved {
			consider(n, t)
		}
	}

	if name == "" {
		return "", "", false
	}
	return name, formatTime(best), true
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
		code := 1
		if e, ok := errors.AsType[exitCodeError](err); ok {
			code = e.code
		}
		os.Exit(code)
	}
}

// exitCodeError makes the process exit with code instead of the default 1.
type exitCodeError struct {
	err  error
	code int
}

func (e exitCodeError) Error() string { return e.err.Error() }
func (e exitCodeError) Unwrap() error { return e.err }
