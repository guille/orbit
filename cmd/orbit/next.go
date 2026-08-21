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

			primeNextRuns(applied.Schedules())

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

// primeNextRuns resolves every not-yet-cached schedule in one systemd-analyze
// invocation. Listings should call this up front so they cost one subprocess
// rather than one per distinct schedule.
func primeNextRuns(schedules []string) {
	seen := make(map[string]bool, len(schedules))
	var pending []string
	for _, s := range schedules {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		if _, cached := nextRunCache[s]; !cached {
			pending = append(pending, s)
		}
	}
	if len(pending) == 0 {
		return
	}

	elapses := systemd.NewManager().NextElapses(pending)
	for _, s := range pending {
		t, ok := elapses[s]
		nextRunCache[s] = nextRunResult{t: t, ok: ok}
	}
}

// nextRun resolves a schedule's next trigger time, cached per invocation.
func nextRun(schedule string) (time.Time, bool) {
	r, hit := nextRunCache[schedule]
	if !hit {
		primeNextRuns([]string{schedule})
		r = nextRunCache[schedule]
	}
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
