package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

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
			if applied.IsEmpty() {
				fmt.Println("No tasks or reminders configured (run 'orbit apply' first)")
				return nil
			}

			taskNames := applied.TaskNames()
			sortNatural(taskNames)

			rNames := applied.ReminderNames()
			sortNatural(rNames)

			tbl := newTable(colName, colType, colSchedule, colNextRun)

			for _, name := range taskNames {
				taskConfig := applied.Tasks[name]
				ts := stateStore.GetTaskState(name)
				tbl.add(name, string(kindTask), orNone(taskConfig.Schedule), taskNextRun(taskConfig, ts))
			}

			for _, name := range rNames {
				reminderConfig := applied.Reminders[name]
				rs := stateStore.GetReminderState(name)
				tbl.add(name, string(kindReminder), orNone(reminderConfig.Schedule), reminderNextRun(reminderConfig, rs))
			}

			fmt.Print(tbl)
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
