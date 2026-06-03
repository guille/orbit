package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/state"
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

// nextRunCache avoids repeated systemd-analyze calls for the same schedule expression within a single CLI invocation.
var nextRunCache = map[string]string{}

// resolveNextRun uses systemd-analyze calendar to determine the next trigger time
// for a given OnCalendar expression. Falls back to showing the raw schedule.
// Results are cached per schedule string within a single process invocation.
func resolveNextRun(schedule string) string {
	if cached, ok := nextRunCache[schedule]; ok {
		return cached
	}
	result := resolveNextRunUncached(schedule)
	nextRunCache[schedule] = result
	return result
}

// resolveNextRunUncached calls systemd-analyze calendar to determine the next trigger time for a schedule expression.
func resolveNextRunUncached(schedule string) string {
	cmd := exec.Command("systemd-analyze", "calendar", schedule, "--iterations=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return schedule + " (could not resolve)"
	}

	for line := range strings.SplitSeq(string(output), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "Next elapse:"); ok {
			raw := strings.TrimSpace(after)
			t, err := parseSystemdTime(raw)
			if err != nil {
				return raw
			}
			return formatTime(t)
		}
	}

	return schedule
}

// parseSystemdTime parses systemd-analyze calendar output timestamps.
func parseSystemdTime(s string) (time.Time, error) {
	// systemd uses "Day YYYY-MM-DD HH:MM:SS TZ"
	// Try common layouts
	layouts := []string{
		"Mon 2006-01-02 15:04:05 MST",
		"Mon 2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05 MST",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %s", s)
}
