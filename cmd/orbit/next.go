package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/state"
	"go.guillerg.dev/orbit/internal/systemd"
)

func nextCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "next",
		Aliases: []string{"n"},
		Short:   "Show next scheduled run for all tasks and reminders",
		Long:    `Display the next scheduled execution time for each configured task and reminder.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			stateStore, err := newState()
			if err != nil {
				return err
			}

			applied := stateStore.GetAppliedConfig()
			if applied == nil || (len(applied.Tasks) == 0 && len(applied.Reminders) == 0) {
				fmt.Println("No tasks or reminders configured (run 'orbit apply' first)")
				return nil
			}

			fmt.Printf("%-20s %-10s %-20s %-30s\n", "NAME", "TYPE", "SCHEDULE", "NEXT RUN")
			fmt.Printf("%-20s %-10s %-20s %-30s\n", "----", "----", "--------", "--------")

			taskNames := make([]string, 0, len(applied.Tasks))
			for name := range applied.Tasks {
				taskNames = append(taskNames, name)
			}
			sortNatural(taskNames)

			rNames := make([]string, 0, len(applied.Reminders))
			for name := range applied.Reminders {
				rNames = append(rNames, name)
			}
			sortNatural(rNames)

			for _, name := range taskNames {
				taskConfig := applied.Tasks[name]
				ts := stateStore.GetTaskState(name)
				if ts.Disabled {
					fmt.Printf("%-20s %-10s %-20s %-30s\n", name, kindTask, taskConfig.Schedule, dim("(disabled)"))
					continue
				}
				if taskConfig.Schedule == "" {
					fmt.Printf("%-20s %-10s %-20s %-30s\n", name, kindTask, "(manual)", "-")
					continue
				}
				nextRun := resolveNextRun(taskConfig.Schedule)
				fmt.Printf("%-20s %-10s %-20s %-30s\n", name, kindTask, taskConfig.Schedule, nextRun)
			}

			for _, name := range rNames {
				reminderConfig := applied.Reminders[name]
				rs := stateStore.GetReminderState(name)

				if rs.Disabled {
					fmt.Printf("%-20s %-10s %-20s %-30s\n", name, kindReminder, reminderConfig.Schedule, dim("(disabled)"))
					continue
				}

				if rs.State == state.StateSnoozed && rs.SnoozedUntil != nil && rs.SnoozedUntil.After(time.Now()) {
					nextRun := formatTime(*rs.SnoozedUntil) + " (snoozed)"
					fmt.Printf("%-20s %-10s %-20s %-30s\n", name, kindReminder, reminderConfig.Schedule, nextRun)
				} else {
					nextRun := resolveNextRun(reminderConfig.Schedule)
					fmt.Printf("%-20s %-10s %-20s %-30s\n", name, kindReminder, reminderConfig.Schedule, nextRun)
				}
			}
			return nil
		},
	}
}

type nextRunResult struct {
	t  time.Time
	ok bool
}

// nextRunCache memoizes schedule resolution within a single CLI invocation.
var nextRunCache = map[string]nextRunResult{}

// nextRun resolves a schedule's next trigger time, cached per invocation.
func nextRun(schedule string) (time.Time, bool) {
	if r, hit := nextRunCache[schedule]; hit {
		return r.t, r.ok
	}
	t, err := systemd.NewManager().NextElapse(schedule)
	r := nextRunResult{t: t, ok: err == nil}
	nextRunCache[schedule] = r
	return r.t, r.ok
}

// resolveNextRun returns a human-friendly next-run string for a schedule.
func resolveNextRun(schedule string) string {
	t, ok := nextRun(schedule)
	if !ok {
		return schedule + " (could not resolve)"
	}
	return formatTime(t)
}
