package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/state"
)

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all tasks and reminders",
		Long:    `Show all configured tasks and reminders with their type, schedule, next run, and status.`,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			stateStore, err := newState()
			if err != nil {
				return err
			}

			applied := stateStore.GetAppliedConfig()
			if applied == nil || (len(applied.Tasks) == 0 && len(applied.Reminders) == 0) {
				fmt.Println("Nothing configured (run 'orbit apply' first)")
				return nil
			}

			type row struct {
				name     string
				kind     entryKind
				schedule string
				nextRun  string
				status   string
			}

			var rows []row

			for name, taskConfig := range applied.Tasks {
				ts := stateStore.GetTaskState(name)

				var nextRunStr string
				if ts.Disabled {
					nextRunStr = dim("(disabled)")
				} else {
					nextRunStr = "(manual)"
					if taskConfig.Schedule != "" {
						nextRunStr = resolveNextRun(taskConfig.Schedule)
					}
				}

				rows = append(rows, row{
					name:     name,
					kind:     kindTask,
					schedule: taskConfig.Schedule,
					nextRun:  nextRunStr,
					status:   taskStatusString(ts),
				})
			}

			for name, reminderConfig := range applied.Reminders {
				rs := stateStore.GetReminderState(name)

				var nextRunStr, statusStr string
				if rs.Disabled {
					nextRunStr = dim("(disabled)")
					statusStr = dim("disabled")
				} else {
					nextRunStr = resolveNextRun(reminderConfig.Schedule)
					statusStr = reminderStatusString(rs)
				}

				rows = append(rows, row{
					name:     name,
					kind:     kindReminder,
					schedule: reminderConfig.Schedule,
					nextRun:  nextRunStr,
					status:   statusStr,
				})
			}

			sort.Slice(rows, func(i, j int) bool {
				if rows[i].kind != rows[j].kind {
					return rows[i].kind == kindTask
				}
				return naturalLess(rows[i].name, rows[j].name)
			})

			fmt.Printf("%-20s %-10s %-20s %-30s %-15s\n", "NAME", "TYPE", "SCHEDULE", "NEXT RUN", "STATUS")
			fmt.Printf("%-20s %-10s %-20s %-30s %-15s\n", "----", "----", "--------", "--------", "------")
			for _, r := range rows {
				scheduleStr := r.schedule
				if scheduleStr == "" {
					scheduleStr = "(none)"
				}
				fmt.Printf("%-20s %-10s %-20s %s %s\n", r.name, r.kind, scheduleStr, padRight(r.nextRun, 30), r.status)
			}

			return nil
		},
	}
}

// reminderStatusString returns a display string for a reminder's current state.
func reminderStatusString(rs state.ReminderState) string {
	display := colorizeReminderState(rs.State)
	if rs.OverdueCount > 1 && (rs.State == state.StatePending || rs.State == state.StateSnoozed) {
		display += fmt.Sprintf(" (%d overdue)", rs.OverdueCount)
	}
	return display
}

// taskStatusString returns a colored display string for a task's current status.
func taskStatusString(ts state.TaskState) string {
	switch {
	case ts.Disabled:
		return dim("disabled")
	case ts.ConsecutiveFailures > 0:
		return red(fmt.Sprintf("failed (%d)", ts.ConsecutiveFailures))
	case ts.LastRun.IsZero():
		return dim("new")
	default:
		return green("ok")
	}
}
