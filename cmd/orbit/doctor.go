package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/config"
	"go.guillerg.dev/orbit/internal/state"
	"go.guillerg.dev/orbit/internal/systemd"
)

// doctorCheckNum numbers diagnostic checks sequentially in output.
var doctorCheckNum int

// nextCheck increments the check counter and prints a numbered check label.
func nextCheck(label string) {
	doctorCheckNum++
	fmt.Printf("%d. %s... ", doctorCheckNum, label)
}

func doctorCmd() *cobra.Command {
	return withConfigFlag(&cobra.Command{
		Use:     "doctor",
		Aliases: []string{"doc"},
		Short:   "Check systemd unit health, config validity, etc",
		Long:    `Perform health checks on the orbit system: validate config, check systemd unit status, detect any issues.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			doctorCheckNum = 0
			fmt.Println("Running system health checks...")

			configPath, err := getConfigPathFromCmd(cmd)
			if err != nil {
				return err
			}
			cfg, err := checkConfigFile(configPath)
			if err != nil {
				return err
			}

			allPassed := checkConfigValid(cfg)

			stateStore, err := newState()
			if err != nil {
				return err
			}
			applied := stateStore.GetAppliedConfig()

			allPassed = checkAppliedConfig(cfg, applied) && allPassed
			allPassed = checkSystemdUnitsDrift(cfg, applied) && allPassed
			allPassed = checkTaskStates(cfg, applied, stateStore) && allPassed
			allPassed = checkReminderStates(cfg, applied, stateStore) && allPassed
			allPassed = checkSentinelFile(stateStore) && allPassed
			allPassed = checkServiceUnits(applied) && allPassed
			allPassed = checkTimerStates(applied, stateStore) && allPassed
			checkDisabledUnits(applied, stateStore)

			if allPassed {
				fmt.Printf("\n%s\n", green("All checks passed!"))
				return nil
			}
			fmt.Printf("\n%s\n", yellow("Some checks failed or have warnings."))
			return fmt.Errorf("health checks failed")
		},
	})
}

// checkConfigFile loads and returns the config, or a fatal error.
func checkConfigFile(configPath string) (*config.Config, error) {
	nextCheck("Checking configuration file")
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Printf("%s: %v\n", red("FAIL"), err)
		fmt.Println("\nCannot continue without a valid configuration.")
		return nil, fmt.Errorf("configuration check failed: %w", err)
	}
	fmt.Println(green("PASS"))
	return cfg, nil
}

// checkConfigValid validates the config. Returns true if passed.
func checkConfigValid(cfg *config.Config) bool {
	nextCheck("Validating configuration")
	if err := cfg.Validate(); err != nil {
		fmt.Printf("%s: %v\n", red("FAIL"), err)
		return false
	}
	fmt.Println(green("PASS"))
	return true
}

// checkAppliedConfig compares user config against applied config.
func checkAppliedConfig(cfg *config.Config, applied *state.AppliedConfig) bool {
	nextCheck("Checking applied config")
	if applied == nil {
		fmt.Printf("%s: no applied config found (run 'orbit apply')\n", yellow("WARNING"))
		return false
	}

	cs := diffConfig(cfg, applied)
	if len(cs.changes) == 0 {
		fmt.Println(green("PASS"))
		return true
	}

	fmt.Println()
	for _, c := range cs.changes {
		switch c.action {
		case actionCreate:
			fmt.Printf("   %s: %s %q not yet applied\n", yellow("DRIFT"), c.kind, c.name)
		case actionUpdate:
			fmt.Printf("   %s: %s %q config changed since last apply\n", yellow("DRIFT"), c.kind, c.name)
		case actionRemove:
			fmt.Printf("   %s: %s %q removed from config but still applied\n", yellow("DRIFT"), c.kind, c.name)
		}
	}
	fmt.Println("   Run 'orbit apply' to reconcile.")
	return false
}

// checkSystemdUnitsDrift verifies systemd units match the applied config.
func checkSystemdUnitsDrift(cfg *config.Config, applied *state.AppliedConfig) bool {
	nextCheck("Checking systemd units for drift")
	manager := systemd.NewManager()
	existingUnits, err := manager.ListUnits()
	if err != nil {
		fmt.Printf("%s: %v\n", red("FAIL"), err)
		return false
	}

	desiredUnits, ok := generateDesiredUnits(manager, applied)
	if !ok {
		return false
	}

	changes := manager.ClassifyChanges(desiredUnits, existingUnits)
	hasDrift := false

	for _, u := range changes.Create {
		if !hasDrift {
			fmt.Println()
			hasDrift = true
		}
		fmt.Printf("   %s: %s\n", red("MISSING"), u.Name)
	}
	for _, u := range changes.Update {
		if !hasDrift {
			fmt.Println()
			hasDrift = true
		}
		fmt.Printf("   %s: %s (content differs)\n", yellow("DRIFTED"), u.Name)
	}
	for _, u := range changes.Remove {
		// Snooze timers are transient — not orphans.
		if strings.HasPrefix(u.Name, "orbit-snooze-") {
			continue
		}
		if !hasDrift {
			fmt.Println()
			hasDrift = true
		}
		fmt.Printf("   %s:  %s (not in config)\n", yellow("ORPHAN"), u.Name)
	}

	if hasDrift {
		fmt.Println("   Run 'orbit apply --force' to reconcile.")
		return false
	}
	fmt.Println(green("PASS"))
	return true
}

// generateDesiredUnits builds the expected units from applied config.
func generateDesiredUnits(manager *systemd.Manager, applied *state.AppliedConfig) ([]systemd.Unit, bool) {
	var units []systemd.Unit
	ok := true

	if applied == nil {
		fmt.Println(dim("SKIP") + " (no applied config)")
		return nil, false
	}

	for name, t := range applied.Tasks {
		u, err := manager.GenerateTaskUnits(name, t.Schedule, t.OnMissed, applied.OrbitBin)
		if err != nil {
			fmt.Printf("\n   %s: Error generating units for task %s: %v\n", red("FAIL"), name, err)
			ok = false
			continue
		}
		units = append(units, u...)
	}
	for name, r := range applied.Reminders {
		u, err := manager.GenerateReminderUnits(name, r.Schedule, applied.OrbitBin)
		if err != nil {
			fmt.Printf("\n   %s: Error generating units for reminder %s: %v\n", red("FAIL"), name, err)
			ok = false
			continue
		}
		units = append(units, u...)
	}
	return units, ok
}

// checkTaskStates checks for tasks with consecutive failures.
func checkTaskStates(cfg *config.Config, applied *state.AppliedConfig, stateStore *state.State) bool {
	nextCheck("Checking task states")
	names := collectNames(
		mapKeys(cfg.Tasks),
		appliedTaskNames(applied),
	)

	hasErrors := false
	for _, name := range names {
		ts := stateStore.GetTaskState(name)
		if ts.ConsecutiveFailures > 0 {
			if !hasErrors {
				fmt.Println()
				hasErrors = true
			}
			fmt.Printf("   %s: %s has %d consecutive failures\n", red("FAIL"), name, ts.ConsecutiveFailures)
			fmt.Printf("         Run 'orbit task logs %s' to investigate.\n", name)
		}
	}
	if hasErrors {
		return false
	}
	fmt.Println(green("PASS"))
	return true
}

// checkReminderStates checks for overdue reminders.
func checkReminderStates(cfg *config.Config, applied *state.AppliedConfig, stateStore *state.State) bool {
	nextCheck("Checking reminder states")
	names := collectNames(
		mapKeys(cfg.Reminders),
		appliedReminderNames(applied),
	)

	hasIssues := false
	for _, name := range names {
		rs := stateStore.GetReminderState(name)
		if rs.State == state.StatePending && !rs.FiredAt.IsZero() {
			if !hasIssues {
				fmt.Println()
				hasIssues = true
			}
			fmt.Printf("   %s: %s pending since %s\n", blue("INFO"), name, formatTime(rs.FiredAt))
		}
		if rs.State == state.StateSnoozed && rs.SnoozedUntil != nil && rs.SnoozedUntil.Before(time.Now()) {
			if !hasIssues {
				fmt.Println()
				hasIssues = true
			}
			fmt.Printf("   %s: %s snooze expired %s (should have re-fired)\n", yellow("WARNING"), name, formatTime(*rs.SnoozedUntil))
			fmt.Printf("         Run 'orbit reminder ack %s' or 'orbit reminder snooze %s' to resolve.\n", name, name)
		}
	}
	if hasIssues {
		return false
	}
	fmt.Println(green("PASS"))
	return true
}

// checkSentinelFile verifies the sentinel file matches actual pending count.
func checkSentinelFile(stateStore *state.State) bool {
	nextCheck("Checking sentinel file")
	dataDir, err := getDataDir()
	if err != nil {
		fmt.Printf("%s: %v\n", red("ERROR"), err)
		return false
	}
	sentinelPath := filepath.Join(dataDir, "pending")
	actualPending := stateStore.Pending()

	sentinelData, err := os.ReadFile(sentinelPath)
	if err != nil && !os.IsNotExist(err) {
		fmt.Printf("%s: cannot read sentinel file: %v\n", red("FAIL"), err)
		return false
	}

	sentinelExists := err == nil
	if actualPending > 0 && !sentinelExists {
		fmt.Printf("%s: %d pending reminders but sentinel file missing\n", yellow("WARNING"), actualPending)
		return false
	}
	if actualPending == 0 && sentinelExists {
		fmt.Printf("%s: no pending reminders but sentinel file exists (content: %q)\n", yellow("WARNING"), string(sentinelData))
		return false
	}
	if sentinelExists {
		sentinelCount, err := strconv.Atoi(strings.TrimSpace(string(sentinelData)))
		if err != nil {
			fmt.Printf("%s: sentinel file has invalid content: %q\n", yellow("WARNING"), string(sentinelData))
			return false
		}
		if sentinelCount != actualPending {
			fmt.Printf("%s: sentinel says %d but state has %d pending\n", yellow("WARNING"), sentinelCount, actualPending)
			return false
		}
	}
	fmt.Println(green("PASS"))
	return true
}

// checkServiceUnits runs systemd-analyze verify on all deployed service unit
// files to detect configuration errors (bad ExecStart paths, missing binaries, etc).
func checkServiceUnits(applied *state.AppliedConfig) bool {
	nextCheck("Verifying service units")
	if applied == nil {
		fmt.Println(dim("SKIP") + " (no applied config)")
		return true
	}

	manager := systemd.NewManager()
	unitDir := manager.UnitDir()

	var paths []string
	for name := range applied.Tasks {
		paths = append(paths, filepath.Join(unitDir, systemd.TaskServiceName(name)))
	}
	for name := range applied.Reminders {
		paths = append(paths, filepath.Join(unitDir, systemd.ReminderServiceName(name)))
	}

	if len(paths) == 0 {
		fmt.Println(green("PASS"))
		return true
	}

	output, err := manager.VerifyUnits(paths...)
	if err != nil {
		fmt.Println(red("FAIL"))
		fmt.Print(output)
		return false
	}
	fmt.Println(green("PASS"))
	return true
}

// checkTimerStates verifies all timers are active and enabled.
func checkTimerStates(applied *state.AppliedConfig, stateStore *state.State) bool {
	nextCheck("Checking timer states")
	if applied == nil {
		fmt.Println(dim("SKIP") + " (no applied config)")
		return true
	}

	var timerNames []string
	for name, cfg := range applied.Tasks {
		if cfg.Schedule == "" {
			continue
		}
		ts := stateStore.GetTaskState(name)
		if ts.Disabled {
			continue
		}
		timerNames = append(timerNames, systemd.TaskTimerName(name))
	}
	for name := range applied.Reminders {
		rs := stateStore.GetReminderState(name)
		if rs.Disabled {
			continue
		}
		timerNames = append(timerNames, systemd.ReminderTimerName(name))
	}

	if len(timerNames) == 0 {
		fmt.Println(green("PASS"))
		return true
	}

	activeArgs := append([]string{"--user", "is-active"}, timerNames...)
	activeOut, _ := exec.Command("systemctl", activeArgs...).Output()
	activeLines := strings.Split(strings.TrimSpace(string(activeOut)), "\n")

	enabledArgs := append([]string{"--user", "is-enabled"}, timerNames...)
	enabledOut, _ := exec.Command("systemctl", enabledArgs...).Output()
	enabledLines := strings.Split(strings.TrimSpace(string(enabledOut)), "\n")

	var problems []string
	for i, name := range timerNames {
		active := i < len(activeLines) && strings.TrimSpace(activeLines[i]) == "active"
		enabled := i < len(enabledLines) && strings.TrimSpace(enabledLines[i]) == "enabled"

		if !active && !enabled {
			problems = append(problems, fmt.Sprintf("%s is not active or enabled", name))
		} else if !active {
			problems = append(problems, fmt.Sprintf("%s is enabled but not active", name))
		} else if !enabled {
			problems = append(problems, fmt.Sprintf("%s is active but not enabled (won't survive reboot)", name))
		}
	}

	if len(problems) > 0 {
		fmt.Println(red("FAIL"))
		for _, p := range problems {
			fmt.Printf("   %s: %s\n", red("FAIL"), p)
		}
		fmt.Println("   Run 'orbit apply --force' to re-enable or check 'systemctl --user status <timer>'.")
		return false
	}
	fmt.Println(green("PASS"))
	return true
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func appliedTaskNames(applied *state.AppliedConfig) []string {
	if applied == nil {
		return nil
	}
	return mapKeys(applied.Tasks)
}

func appliedReminderNames(applied *state.AppliedConfig) []string {
	if applied == nil {
		return nil
	}
	return mapKeys(applied.Reminders)
}

// collectNames deduplicates and returns a sorted list of names.
func collectNames(lists ...[]string) []string {
	seen := make(map[string]bool)
	for _, list := range lists {
		for _, name := range list {
			seen[name] = true
		}
	}
	names := mapKeys(seen)
	sortNatural(names)
	return names
}

// checkDisabledUnits reports how many units are currently disabled (always passes).
func checkDisabledUnits(applied *state.AppliedConfig, stateStore *state.State) {
	if applied == nil {
		return
	}
	var disabledTasks, disabledReminders int
	for name := range applied.Tasks {
		if stateStore.GetTaskState(name).Disabled {
			disabledTasks++
		}
	}
	for name := range applied.Reminders {
		if stateStore.GetReminderState(name).Disabled {
			disabledReminders++
		}
	}
	if disabledTasks == 0 && disabledReminders == 0 {
		return
	}
	nextCheck("Checking disabled units")
	parts := []string{}
	if disabledTasks > 0 {
		parts = append(parts, fmt.Sprintf("%d task%s", disabledTasks, plural(disabledTasks)))
	}
	if disabledReminders > 0 {
		parts = append(parts, fmt.Sprintf("%d reminder%s", disabledReminders, plural(disabledReminders)))
	}
	fmt.Println()
	fmt.Printf("   %s: %s disabled\n", blue("INFO"), strings.Join(parts, ", "))
}
