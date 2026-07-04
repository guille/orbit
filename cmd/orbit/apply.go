package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

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

	var allUnits []systemd.Unit
	for name, t := range cfg.Tasks {
		units, err := systemd.GenerateTaskUnits(name, t.Schedule, t.OnMissed, cfg.OrbitBin)
		if err != nil {
			return fmt.Errorf("generating units for task %s: %w", name, err)
		}
		allUnits = append(allUnits, units...)
	}
	for name, r := range cfg.Reminders {
		units, err := systemd.GenerateReminderUnits(name, r.Schedule, cfg.OrbitBin)
		if err != nil {
			return fmt.Errorf("generating units for reminder %s: %w", name, err)
		}
		allUnits = append(allUnits, units...)
	}

	if len(allUnits) > 0 {
		tmpDir, cleanup, err := manager.WriteUnits(allUnits)
		if err != nil {
			return fmt.Errorf("writing units: %w", err)
		}
		defer cleanup()

		var unitPaths []string
		for _, u := range allUnits {
			unitPaths = append(unitPaths, filepath.Join(tmpDir, u.Name))
		}

		output, err := manager.VerifyUnits(unitPaths...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n%s", red("unit verification failed"), output)
			return fmt.Errorf("unit verification failed")
		}

		if err := manager.InstallUnits(allUnits, tmpDir); err != nil {
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

	stateStore.SetAppliedConfig(toAppliedConfig(cfg))
	if err := stateStore.Save(); err != nil {
		return fmt.Errorf("saving applied config: %w", err)
	}

	// Respect disabled state: stop timers for disabled entries
	disabledTimers := disabledTimerNames(cfg, stateStore)
	if len(disabledTimers) > 0 {
		manager.StopAndDisableTimers(disabledTimers)
	}

	fmt.Printf("Done. %d created, %d updated, %d removed, %d unchanged.\n",
		cs.nCreate, cs.nUpdate, cs.nRemove, cs.nUnchanged)
	if len(disabledTimers) > 0 {
		fmt.Printf("%s\n", dim(fmt.Sprintf("(%d entries disabled)", len(disabledTimers))))
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
		old.Retry.Delay != new.Retry.Delay
}

func reminderChanged(old state.AppliedReminderConfig, new config.ReminderConfig) bool {
	return old.Command != new.Command ||
		old.Schedule != new.Schedule ||
		old.Message != new.Message ||
		old.Snooze != new.Snooze ||
		old.Check != new.Check
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

// disabledTimerNames returns the timer unit names for entries marked disabled,
// sorted for stable ordering.
func disabledTimerNames(cfg *config.Config, sr stateReader) []string {
	var names []string
	for name := range cfg.Tasks {
		if sr.GetTaskState(name).Disabled {
			names = append(names, systemd.TaskTimerName(name))
		}
	}
	for name := range cfg.Reminders {
		if sr.GetReminderState(name).Disabled {
			names = append(names, systemd.ReminderTimerName(name))
		}
	}
	sortNatural(names)
	return names
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
