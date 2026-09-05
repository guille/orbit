package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/config"
	"go.guillerg.dev/orbit/internal/state"
	"go.guillerg.dev/orbit/internal/systemd"
)

// changeAction describes what operation a configChange represents.
type changeAction string

const (
	actionCreate changeAction = "create"
	actionUpdate changeAction = "update"
	actionRemove changeAction = "remove"
)

// configChange represents a change to a single task or reminder.
type configChange struct {
	name   string       // logical name
	kind   entryKind    // kindTask or kindReminder
	action changeAction // actionCreate, actionUpdate, or actionRemove
	// For updates, the old and new values:
	oldTask     *state.AppliedTaskConfig
	newTask     *config.TaskConfig
	oldReminder *state.AppliedReminderConfig
	newReminder *config.ReminderConfig
	oldOrbitBin string
	newOrbitBin string
}

// configChangeSet groups all config-level changes.
type configChangeSet struct {
	changes    []configChange
	nCreate    int
	nUpdate    int
	nRemove    int
	nUnchanged int
}

func applyCmd() *cobra.Command {
	var force bool

	cmd := withConfigFlag(&cobra.Command{
		Use:     "apply",
		Aliases: []string{"a", "ap"},
		Short:   "Reconcile config with systemd units",
		Long:    `Parse the configuration file and create/update/delete systemd user units to match the declared configuration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}

			stateStore, err := newState()
			if err != nil {
				return err
			}

			return runApply(cfg, stateStore, nil, force)
		},
	})

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Regenerate all unit files even if config is unchanged")

	return cmd
}

// runApply performs the actual apply operation for a given config.
// If precomputed is non-nil, it uses that changeset instead of recomputing.
// If force is true, units are regenerated even when config is unchanged.
func runApply(cfg *config.Config, stateStore *state.State, precomputed *configChangeSet, force bool) error {
	ensureEmbeddedAssets(stateStore)

	var cs configChangeSet
	if precomputed != nil {
		cs = *precomputed
	} else {
		cs = diffConfig(cfg, stateStore.GetAppliedConfig())
	}

	if len(cs.changes) == 0 && !force {
		t := len(cfg.Tasks)
		r := len(cfg.Reminders)
		parts := []string{}
		if t > 0 {
			parts = append(parts, fmt.Sprintf("%d task%s", t, plural(t)))
		}
		if r > 0 {
			parts = append(parts, fmt.Sprintf("%d reminder%s", r, plural(r)))
		}
		if len(parts) > 0 {
			fmt.Printf("All %s up to date.\n", joinAnd(parts))
		} else {
			fmt.Println("No changes needed. Configuration is empty.")
		}
		return nil
	}

	if len(cs.changes) > 0 && precomputed == nil {
		printConfigChanges(cs)
		fmt.Println()
	}
	if force && len(cs.changes) == 0 {
		fmt.Println("No config changes, regenerating unit files...")
	}

	manager := systemd.NewManager()

	units, err := unitsToWrite(cfg, cs, force)
	if err != nil {
		return err
	}

	if len(units) > 0 {
		tmpDir, cleanup, err := manager.WriteUnits(units)
		if err != nil {
			return fmt.Errorf("writing units: %w", err)
		}
		defer cleanup()

		var unitPaths []string
		for _, u := range units {
			unitPaths = append(unitPaths, filepath.Join(tmpDir, u.Name))
		}

		output, err := manager.VerifyUnits(unitPaths...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n%s", red("unit verification failed"), output)
			return fmt.Errorf("unit verification failed")
		}

		if err := manager.InstallUnits(units, tmpDir); err != nil {
			return fmt.Errorf("installing units: %w", err)
		}
	}

	toRemove, removed := unitsToRemove(cs)
	if len(toRemove) > 0 {
		if err := manager.RemoveUnits(toRemove); err != nil {
			return fmt.Errorf("removing obsolete units: %w", err)
		}
	}
	for _, c := range removed {
		switch c.kind {
		case kindTask:
			stateStore.DeleteTaskState(c.name)
		case kindReminder:
			stateStore.DeleteReminderState(c.name)
		}
	}
	unskipped := clearSkipsOnScheduleChange(cs, stateStore)

	stateStore.SetAppliedConfig(toAppliedConfig(cfg))
	if err := stateStore.Save(); err != nil {
		return fmt.Errorf("saving applied config: %w", err)
	}

	// Installing a timer enables it, so disabled entries that were just written
	// have to be put back.
	if timers := timersToDisable(cfg, stateStore, cs, force); len(timers) > 0 {
		manager.StopAndDisableTimers(timers)
	}

	fmt.Printf("Done. %d created, %d updated, %d removed, %d unchanged.\n",
		cs.nCreate, cs.nUpdate, cs.nRemove, cs.nUnchanged)
	for _, name := range unskipped {
		fmt.Printf("%s\n", dim(fmt.Sprintf("(skip on '%s' cleared: schedule changed)", name)))
	}
	if n := disabledEntryCount(cfg, stateStore); n > 0 {
		fmt.Printf("%s\n", dim(fmt.Sprintf("(%d entries disabled)", n)))
	}
	return nil
}

// diffConfig compares the user config against the last applied config.
func diffConfig(cfg *config.Config, applied *state.AppliedConfig) configChangeSet {
	var cs configChangeSet

	if applied == nil {
		applied = &state.AppliedConfig{
			Tasks:     make(map[string]state.AppliedTaskConfig),
			Reminders: make(map[string]state.AppliedReminderConfig),
		}
	}
	binPathChanged := applied.OrbitBin != cfg.OrbitBin

	for name, t := range cfg.Tasks {
		old, existed := applied.Tasks[name]
		if !existed {
			cs.changes = append(cs.changes, configChange{
				name: name, kind: kindTask, action: actionCreate,
				newTask: &t,
			})
			cs.nCreate++
		} else if taskChanged(old, t) || binPathChanged {
			cs.changes = append(cs.changes, configChange{
				name: name, kind: kindTask, action: actionUpdate,
				oldTask: &old, newTask: &t,
				oldOrbitBin: applied.OrbitBin, newOrbitBin: cfg.OrbitBin,
			})
			cs.nUpdate++
		} else {
			cs.nUnchanged++
		}
	}
	for name := range applied.Tasks {
		if _, exists := cfg.Tasks[name]; !exists {
			cs.changes = append(cs.changes, configChange{
				name: name, kind: kindTask, action: actionRemove,
			})
			cs.nRemove++
		}
	}

	for name, r := range cfg.Reminders {
		old, existed := applied.Reminders[name]
		if !existed {
			cs.changes = append(cs.changes, configChange{
				name: name, kind: kindReminder, action: actionCreate,
				newReminder: &r,
			})
			cs.nCreate++
		} else if reminderChanged(old, r) || binPathChanged {
			cs.changes = append(cs.changes, configChange{
				name: name, kind: kindReminder, action: actionUpdate,
				oldReminder: &old, newReminder: &r,
				oldOrbitBin: applied.OrbitBin, newOrbitBin: cfg.OrbitBin,
			})
			cs.nUpdate++
		} else {
			cs.nUnchanged++
		}
	}
	for name := range applied.Reminders {
		if _, exists := cfg.Reminders[name]; !exists {
			cs.changes = append(cs.changes, configChange{
				name: name, kind: kindReminder, action: actionRemove,
			})
			cs.nRemove++
		}
	}

	actionOrder := map[changeAction]int{actionCreate: 0, actionUpdate: 1, actionRemove: 2}
	sort.Slice(cs.changes, func(i, j int) bool {
		if cs.changes[i].action != cs.changes[j].action {
			return actionOrder[cs.changes[i].action] < actionOrder[cs.changes[j].action]
		}
		if cs.changes[i].kind != cs.changes[j].kind {
			return cs.changes[i].kind < cs.changes[j].kind
		}
		return naturalLess(cs.changes[i].name, cs.changes[j].name)
	})

	return cs
}

func taskChanged(old state.AppliedTaskConfig, new config.TaskConfig) bool {
	return old.Command != new.Command ||
		old.Schedule != new.Schedule ||
		old.OnMissed != new.OnMissed ||
		old.Retry.Attempts != new.Retry.GetAttempts() ||
		old.Retry.Delay != new.Retry.Delay ||
		old.IfFailed.Command != new.IfFailed.Command ||
		old.IfFailed.After != new.IfFailed.GetAfter()
}

func reminderChanged(old state.AppliedReminderConfig, new config.ReminderConfig) bool {
	return old.Command != new.Command ||
		old.Schedule != new.Schedule ||
		old.Message != new.Message ||
		old.Snooze != new.Snooze ||
		old.Check != new.Check
}

// changedEntries returns the task and reminder names the changeset creates or
// updates, i.e. those whose unit files need regenerating.
func changedEntries(cs configChangeSet) (tasks, reminders map[string]bool) {
	tasks = make(map[string]bool)
	reminders = make(map[string]bool)
	for _, c := range cs.changes {
		if c.action == actionRemove {
			continue
		}
		switch c.kind {
		case kindTask:
			tasks[c.name] = true
		case kindReminder:
			reminders[c.name] = true
		}
	}
	return tasks, reminders
}

// unitsToWrite generates the units to install. Only entries the changeset
// created or updated are regenerated: reinstalling a byte-identical unit still
// costs a systemd-analyze verify and an enable round trip, both of which grow
// with the size of the config. force regenerates everything, which is what
// repairs unit files that drifted on disk without a config change.
func unitsToWrite(cfg *config.Config, cs configChangeSet, force bool) ([]systemd.Unit, error) {
	changedTasks, changedReminders := changedEntries(cs)

	var units []systemd.Unit
	for name, t := range cfg.Tasks {
		if !force && !changedTasks[name] {
			continue
		}
		u, err := systemd.GenerateTaskUnits(name, t.Schedule, t.OnMissed, cfg.OrbitBin)
		if err != nil {
			return nil, fmt.Errorf("generating units for task %s: %w", name, err)
		}
		units = append(units, u...)
	}
	for name, r := range cfg.Reminders {
		if !force && !changedReminders[name] {
			continue
		}
		u, err := systemd.GenerateReminderUnits(name, r.Schedule, cfg.OrbitBin)
		if err != nil {
			return nil, fmt.Errorf("generating units for reminder %s: %w", name, err)
		}
		units = append(units, u...)
	}
	return units, nil
}

// unitsToRemove returns the units to uninstall for a changeset, along with the
// removed entries whose stored state should be deleted. A task that merely lost
// its schedule keeps its service but drops its now-obsolete timer.
func unitsToRemove(cs configChangeSet) (units []systemd.Unit, removed []configChange) {
	for _, c := range cs.changes {
		switch {
		case c.action == actionRemove && c.kind == kindTask:
			units = append(units,
				systemd.Unit{Name: systemd.TaskServiceName(c.name)},
				systemd.Unit{Name: systemd.TaskTimerName(c.name)},
			)
			removed = append(removed, c)
		case c.action == actionRemove && c.kind == kindReminder:
			units = append(units,
				systemd.Unit{Name: systemd.ReminderServiceName(c.name)},
				systemd.Unit{Name: systemd.ReminderTimerName(c.name)},
				systemd.Unit{Name: systemd.SnoozeTimerName(c.name)},
			)
			removed = append(removed, c)
		case c.action == actionUpdate && c.kind == kindTask &&
			c.oldTask != nil && c.oldTask.Schedule != "" &&
			c.newTask != nil && c.newTask.Schedule == "":
			units = append(units, systemd.Unit{Name: systemd.TaskTimerName(c.name)})
		}
	}
	return units, removed
}

// stateReader reads per-entry state needed to decide which timers to stop.
type stateReader interface {
	GetTaskState(name string) state.TaskState
	GetReminderState(name string) state.ReminderState
}

// clearSkipsOnScheduleChange drops the skip window of every updated entry whose
// schedule changed, returning their names. A skip means "not the next N of
// this schedule"; against a different schedule the stored instant would cover
// an arbitrary number of firings, or none if the schedule went away.
func clearSkipsOnScheduleChange(cs configChangeSet, stateStore *state.State) []string {
	var names []string
	for _, c := range cs.changes {
		if c.action != actionUpdate {
			continue
		}
		switch c.kind {
		case kindTask:
			ts := stateStore.GetTaskState(c.name)
			if ts.SkipUntil != nil && c.oldTask.Schedule != c.newTask.Schedule {
				ts.SkipUntil = nil
				stateStore.SetTaskState(c.name, ts)
				names = append(names, c.name)
			}
		case kindReminder:
			rs := stateStore.GetReminderState(c.name)
			if rs.SkipUntil != nil && c.oldReminder.Schedule != c.newReminder.Schedule {
				rs.SkipUntil = nil
				stateStore.SetReminderState(c.name, rs)
				names = append(names, c.name)
			}
		}
	}
	sortNatural(names)
	return names
}

// timersToDisable returns the timers apply has to disable after installing:
// those of disabled entries whose units it just wrote, since installing a timer
// also enables it. Entries apply left alone are already disabled, and disabling
// them again would cost a daemon-reload for nothing. Sorted for stable ordering.
func timersToDisable(cfg *config.Config, sr stateReader, cs configChangeSet, force bool) []string {
	changedTasks, changedReminders := changedEntries(cs)

	var names []string
	for name, t := range cfg.Tasks {
		// A manual task has no timer to disable.
		if t.Schedule == "" {
			continue
		}
		if (force || changedTasks[name]) && sr.GetTaskState(name).Disabled {
			names = append(names, systemd.TaskTimerName(name))
		}
	}
	for name := range cfg.Reminders {
		if (force || changedReminders[name]) && sr.GetReminderState(name).Disabled {
			names = append(names, systemd.ReminderTimerName(name))
		}
	}
	sortNatural(names)
	return names
}

// disabledEntryCount counts the entries marked disabled, for the apply summary.
// Unlike timersToDisable this covers the whole config, including entries apply
// did not touch and manual tasks that have no timer at all.
func disabledEntryCount(cfg *config.Config, sr stateReader) int {
	n := 0
	for name := range cfg.Tasks {
		if sr.GetTaskState(name).Disabled {
			n++
		}
	}
	for name := range cfg.Reminders {
		if sr.GetReminderState(name).Disabled {
			n++
		}
	}
	return n
}

// printConfigChanges prints the config-level change set with colors.
func printConfigChanges(cs configChangeSet) {
	for _, c := range cs.changes {
		var prefix string
		switch c.action {
		case actionCreate:
			prefix = green("+ create")
		case actionUpdate:
			prefix = yellow("~ update")
		case actionRemove:
			prefix = red("- remove")
		}

		fmt.Printf("  %s %s %q\n", prefix, c.kind, c.name)

		switch c.action {
		case actionCreate:
			printNewConfigSummary(c)
		case actionUpdate:
			printConfigDiff(c)
		}
	}
}

// printNewConfigSummary shows the config values for a newly created entry.
func printNewConfigSummary(c configChange) {
	switch c.kind {
	case kindTask:
		t := c.newTask
		if t.Schedule != "" {
			fmt.Printf("      schedule:  %s\n", t.Schedule)
		} else {
			fmt.Printf("      schedule:  (manual)\n")
		}
		fmt.Printf("      command:   %s\n", t.Command)
		if t.Schedule != "" {
			fmt.Printf("      on_missed: %s\n", t.OnMissed)
		}
		if t.Retry.GetAttempts() > 0 {
			fmt.Printf("      retry:     %d attempts, %s delay\n", t.Retry.GetAttempts(), t.Retry.Delay)
		}
		if t.IfFailed.Command != "" {
			fmt.Printf("      if_failed: %s\n", t.IfFailed.Command)
			if after := t.IfFailed.GetAfter(); after > 1 {
				fmt.Printf("                 after %d failed cycles\n", after)
			}
		}
	case kindReminder:
		r := c.newReminder
		fmt.Printf("      schedule: %s\n", r.Schedule)
		fmt.Printf("      message:  %s\n", r.Message)
		if r.Command != "" {
			fmt.Printf("      command:  %s\n", r.Command)
		}
		if r.Check != "" {
			fmt.Printf("      check:    %s\n", r.Check)
		}
		if r.Snooze != "" {
			fmt.Printf("      snooze:   %s\n", r.Snooze)
		}
	}
}

// printConfigDiff shows field-level changes for an updated entry.
func printConfigDiff(c configChange) {
	diffField("orbit_bin", c.oldOrbitBin, c.newOrbitBin)
	switch c.kind {
	case kindTask:
		old, new := c.oldTask, c.newTask
		diffField("command", old.Command, new.Command)
		diffField("schedule", old.Schedule, new.Schedule)
		diffField("on_missed", string(old.OnMissed), string(new.OnMissed))
		if old.Retry.Attempts != new.Retry.GetAttempts() {
			fmt.Printf("      %s %s %s\n",
				dim("retry.attempts:"),
				red(fmt.Sprintf("%d", old.Retry.Attempts)),
				green(fmt.Sprintf("-> %d", new.Retry.GetAttempts())))
		}
		diffField("retry.delay", old.Retry.Delay, new.Retry.Delay)
		diffField("if_failed.command", old.IfFailed.Command, new.IfFailed.Command)
		diffField("if_failed.after", intOrEmpty(old.IfFailed.After), intOrEmpty(new.IfFailed.GetAfter()))
	case kindReminder:
		old, new := c.oldReminder, c.newReminder
		diffField("command", old.Command, new.Command)
		diffField("schedule", old.Schedule, new.Schedule)
		diffField("message", old.Message, new.Message)
		diffField("check", old.Check, new.Check)
		diffField("snooze", old.Snooze, new.Snooze)
	}
}

// diffField prints a single field diff if values differ.
func diffField(name, oldVal, newVal string) {
	if oldVal == newVal {
		return
	}
	fmt.Printf("      %s %s %s\n",
		dim(name+":"),
		red(oldVal),
		green("-> "+newVal))
}

// intOrEmpty formats n for diffField, treating 0 as "unset".
func intOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}
