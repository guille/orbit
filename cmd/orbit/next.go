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
	t   time.Time
	ok  bool
	err error
}

// nextRunKey identifies one schedule resolution: the first trigger after
// `after`, or after now when it is zero.
type nextRunKey struct {
	schedule string
	after    time.Time
}

// nextRunCache memoizes schedule resolution within a single CLI invocation.
var nextRunCache = map[nextRunKey]nextRunResult{}

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
		if _, cached := nextRunCache[nextRunKey{schedule: s}]; !cached {
			pending = append(pending, s)
		}
	}
	if len(pending) == 0 {
		return
	}

	elapses := systemd.NewManager().NextElapses(pending)
	for _, s := range pending {
		t, ok := elapses[s]
		nextRunCache[nextRunKey{schedule: s}] = nextRunResult{t: t, ok: ok}
	}
}

// nextRun resolves a schedule's next trigger time, cached per invocation.
func nextRun(schedule string) (time.Time, bool) {
	key := nextRunKey{schedule: schedule}
	r, hit := nextRunCache[key]
	if !hit {
		primeNextRuns([]string{schedule})
		r = nextRunCache[key]
	}
	return r.t, r.ok
}

// nextRunAfter resolves a schedule's first trigger strictly after the given
// instant, cached per invocation. Each distinct instant costs its own
// systemd-analyze call, since --base-time applies to a whole invocation. The
// error means systemd-analyze could not answer; ok=false means nothing is left.
func nextRunAfter(schedule string, after time.Time) (time.Time, bool, error) {
	key := nextRunKey{schedule, after}
	r, hit := nextRunCache[key]
	if !hit {
		r.t, r.ok, r.err = systemd.NewManager().NextAfter(schedule, after)
		nextRunCache[key] = r
	}
	return r.t, r.ok, r.err
}

// skipResume reports whether a skip on the given schedule is still in force
// and, if so, when firings resume: the first occurrence after skipUntil. A
// skip whose resume point has passed, or whose schedule has nothing left, is
// over and reads as absent. When the schedule cannot be resolved at all the
// skip fails closed: active, with a zero resume time. Running something the
// user asked to skip is the one outcome this feature must never produce.
func skipResume(schedule string, skipUntil *time.Time) (time.Time, bool) {
	if skipUntil == nil || skipUntil.IsZero() || schedule == "" {
		return time.Time{}, false
	}
	resume, ok, err := nextRunAfter(schedule, *skipUntil)
	if err != nil {
		return time.Time{}, true
	}
	if !ok || !resume.After(time.Now()) {
		return time.Time{}, false
	}
	return resume, true
}

// nextFire resolves when a scheduled entry actually fires next, honoring an
// active skip.
func nextFire(schedule string, skipUntil *time.Time) (time.Time, bool) {
	if resume, ok := skipResume(schedule, skipUntil); ok {
		return resume, true
	}
	return nextRun(schedule)
}

// resolveNextRun returns a human-friendly next-run string for a schedule.
func resolveNextRun(schedule string) string {
	t, ok := nextRun(schedule)
	if !ok {
		return schedule + " (could not resolve)"
	}
	return formatTime(t)
}
