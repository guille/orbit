package main

import (
	"fmt"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/config"
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
				tbl.add(name, string(kindTask), orNone(taskConfig.Schedule.String()), taskNextRun(taskConfig, ts))
			}

			for _, name := range rNames {
				reminderConfig := applied.Reminders[name]
				rs := stateStore.GetReminderState(name)
				tbl.add(name, string(kindReminder), orNone(reminderConfig.Schedule.String()), reminderNextRun(reminderConfig, rs))
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

// nextRunKey identifies one expression resolution: the first trigger after
// `after`, or after now when it is zero.
type nextRunKey struct {
	expr  string
	after time.Time
}

// nextRunCache memoizes expression resolution within a single CLI invocation.
var nextRunCache = map[nextRunKey]nextRunResult{}

// primeNextRuns resolves every not-yet-cached expression in one systemd-analyze
// invocation. Listings should call this up front with every entry's
// expressions so they cost one subprocess rather than one per distinct
// expression.
func primeNextRuns(exprs []string) {
	primeNextRunsAfter(exprs, time.Time{})
}

// primeNextRunsAfter is primeNextRuns for triggers strictly after the given
// instant. Each distinct instant costs its own systemd-analyze call, since
// --base-time applies to a whole invocation.
func primeNextRunsAfter(exprs []string, after time.Time) {
	var pending []string
	for _, e := range exprs {
		if _, cached := nextRunCache[nextRunKey{e, after}]; !cached && !slices.Contains(pending, e) {
			pending = append(pending, e)
		}
	}
	if len(pending) == 0 {
		return
	}

	elapses, err := systemd.NewManager().NextElapses(pending, after)
	for _, e := range pending {
		t, answered := elapses[e]
		if err == nil && !answered {
			err = fmt.Errorf("systemd-analyze rejected %q", e)
		}
		nextRunCache[nextRunKey{e, after}] = nextRunResult{t: t, ok: !t.IsZero(), err: err}
	}
}

// nextRun resolves a schedule's next trigger time, cached per invocation.
func nextRun(schedule config.Schedule) (time.Time, bool) {
	t, ok, _ := nextRunAfter(schedule, time.Time{})
	return t, ok
}

// nextRunAfter resolves a schedule's first trigger strictly after the given
// instant (now, if zero): the earliest across its expressions, cached per
// invocation. ok=false means none of them has anything left. The error means
// systemd-analyze could not answer for at least one of them.
func nextRunAfter(schedule config.Schedule, after time.Time) (t time.Time, ok bool, err error) {
	primeNextRunsAfter(schedule, after)
	for _, e := range schedule {
		r := nextRunCache[nextRunKey{e, after}]
		if r.err != nil {
			err = r.err
		}
		if r.ok && (!ok || r.t.Before(t)) {
			t, ok = r.t, true
		}
	}
	return t, ok, err
}

// skipResume reports whether a skip on the given schedule is still in force
// and, if so, when firings resume: the first occurrence after skipUntil. A
// skip whose resume point has passed, or whose schedule has nothing left, is
// over and reads as absent. When the schedule cannot be resolved at all the
// skip fails closed: active, with a zero resume time. Running something the
// user asked to skip is the one outcome this feature must never produce.
func skipResume(schedule config.Schedule, skipUntil *time.Time) (time.Time, bool) {
	if skipUntil == nil || skipUntil.IsZero() || schedule.IsZero() {
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
func nextFire(schedule config.Schedule, skipUntil *time.Time) (time.Time, bool) {
	if resume, ok := skipResume(schedule, skipUntil); ok {
		return resume, true
	}
	return nextRun(schedule)
}

// resolveNextRun returns a human-friendly next-run string for a schedule.
func resolveNextRun(schedule config.Schedule) string {
	t, ok := nextRun(schedule)
	if !ok {
		return schedule.String() + " (could not resolve)"
	}
	return formatTime(t)
}
