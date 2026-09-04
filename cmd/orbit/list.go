package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/systemd"
)

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all tasks and reminders",
		Long:    `Show all configured tasks and reminders with their type, schedule, last run, next run, and status.`,
		Aliases: []string{"ls"},
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

			type row struct {
				name  string
				kind  entryKind
				cells []string
			}

			var rows []row

			primeNextRuns(applied.Schedules())
			failed, _ := systemd.NewManager().FailedServices(applied.TaskNames())

			for name, taskConfig := range applied.Tasks {
				ts := stateStore.GetTaskState(name)
				rows = append(rows, row{name: name, kind: kindTask, cells: []string{
					name,
					string(kindTask),
					orNone(taskConfig.Schedule),
					formatTime(ts.LastRun),
					taskNextRun(taskConfig, ts),
					taskStatusString(ts, taskConfig.Retry.Attempts, failed[name].Failed()),
				}})
			}

			for name, reminderConfig := range applied.Reminders {
				rs := stateStore.GetReminderState(name)
				rows = append(rows, row{name: name, kind: kindReminder, cells: []string{
					name,
					string(kindReminder),
					orNone(reminderConfig.Schedule),
					formatTime(rs.FiredAt),
					reminderNextRun(reminderConfig, rs),
					reminderStatusString(reminderConfig, rs),
				}})
			}

			sort.Slice(rows, func(i, j int) bool {
				if rows[i].kind != rows[j].kind {
					return rows[i].kind == kindTask
				}
				return naturalLess(rows[i].name, rows[j].name)
			})

			tbl := newTable(colName, colType, colSchedule, colLastRun, colNextRun, colStatus)
			for _, r := range rows {
				tbl.add(r.cells...)
			}
			fmt.Print(tbl)

			return nil
		},
	}
}
