package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/guille/orbit/internal/config"
	"github.com/guille/orbit/internal/state"
	"github.com/guille/orbit/internal/systemd"
)

// entryKind distinguishes tasks from reminders.
type entryKind string

const (
	kindTask     entryKind = "task"
	kindReminder entryKind = "reminder"
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

	manager := systemd.NewManager(resolveOrbitBinary())

	var allUnits []systemd.Unit
	for name, t := range cfg.Tasks {
		units, err := manager.GenerateTaskUnits(name, t.Schedule, t.OnMissed)
		if err != nil {
			return fmt.Errorf("generating units for task %s: %w", name, err)
		}
		allUnits = append(allUnits, units...)
	}
	for name, r := range cfg.Reminders {
		units, err := manager.GenerateReminderUnits(name, r.Schedule)
		if err != nil {
			return fmt.Errorf("generating units for reminder %s: %w", name, err)
		}
		allUnits = append(allUnits, units...)
	}

	if len(allUnits) > 0 {
		if err := manager.ApplyUnits(allUnits); err != nil {
			return fmt.Errorf("applying systemd units: %w", err)
		}
	}

	var toRemove []systemd.Unit
	for _, c := range cs.changes {
		if c.action == actionRemove {
			switch c.kind {
			case kindTask:
				toRemove = append(toRemove,
					systemd.Unit{Name: systemd.TaskServiceName(c.name)},
					systemd.Unit{Name: systemd.TaskTimerName(c.name)},
				)
			case kindReminder:
				toRemove = append(toRemove,
					systemd.Unit{Name: systemd.ReminderServiceName(c.name)},
					systemd.Unit{Name: systemd.ReminderTimerName(c.name)},
					systemd.Unit{Name: systemd.SnoozeTimerName(c.name)},
				)
			}
		}
		// A task that lost its schedule still gets its service updated, but the timer must go
		if c.action == actionUpdate && c.kind == kindTask {
			if c.oldTask != nil && c.oldTask.Schedule != "" && c.newTask != nil && c.newTask.Schedule == "" {
				toRemove = append(toRemove,
					systemd.Unit{Name: systemd.TaskTimerName(c.name)},
				)
			}
		}
	}
	if len(toRemove) > 0 {
		if err := manager.RemoveUnits(toRemove); err != nil {
			return fmt.Errorf("removing obsolete units: %w", err)
		}
		for _, c := range cs.changes {
			if c.action != actionRemove {
				continue
			}
			switch c.kind {
			case kindTask:
				stateStore.DeleteTaskState(c.name)
			case kindReminder:
				stateStore.DeleteReminderState(c.name)
			}
		}
	}

	stateStore.SetAppliedConfig(toAppliedConfig(cfg))
	if err := stateStore.Save(); err != nil {
		return fmt.Errorf("saving applied config: %w", err)
	}

	// Respect disabled state: stop timers for disabled entries
	var disabledTimers []string
	for name := range cfg.Tasks {
		ts := stateStore.GetTaskState(name)
		if ts.Disabled {
			disabledTimers = append(disabledTimers, systemd.TaskTimerName(name))
		}
	}
	for name := range cfg.Reminders {
		rs := stateStore.GetReminderState(name)
		if rs.Disabled {
			disabledTimers = append(disabledTimers, systemd.ReminderTimerName(name))
		}
	}
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

	for name, t := range cfg.Tasks {
		old, existed := applied.Tasks[name]
		if !existed {
			cs.changes = append(cs.changes, configChange{
				name: name, kind: kindTask, action: actionCreate,
				newTask: &t,
			})
			cs.nCreate++
		} else if taskChanged(old, t) {
			cs.changes = append(cs.changes, configChange{
				name: name, kind: kindTask, action: actionUpdate,
				oldTask: &old, newTask: &t,
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
		} else if reminderChanged(old, r) {
			cs.changes = append(cs.changes, configChange{
				name: name, kind: kindReminder, action: actionUpdate,
				oldReminder: &old, newReminder: &r,
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
