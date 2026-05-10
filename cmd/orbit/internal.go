package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/guille/orbit/internal/config"
	"github.com/guille/orbit/internal/picker"
	"github.com/guille/orbit/internal/reminder"
	"github.com/guille/orbit/internal/state"
	"github.com/guille/orbit/internal/systemd"
	"github.com/guille/orbit/internal/task"
)

// runInternalCmd is invoked by systemd: orbit _run NAME
func runInternalCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_run NAME",
		Short:  "Internal: run a task",
		Args:   cobra.ExactArgs(1),
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			stateStore, err := newState()
			if err != nil {
				return err
			}

			taskConfig, ok := stateStore.GetAppliedTask(name)
			if !ok {
				return notAppliedErr(kindTask, name)
			}

			runner := task.NewRunner(stateStore)

			if err := runner.Run(name, taskConfig.Command, taskConfig.Retry.ToRetryConfig()); err != nil {
				return fmt.Errorf("task failed: %w", err)
			}
			return nil
		},
	}
}

// notifyInternalCmd is invoked by systemd: orbit _notify NAME
func notifyInternalCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_notify NAME",
		Short:  "Internal: send a reminder notification",
		Args:   cobra.ExactArgs(1),
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			stateStore, err := newState()
			if err != nil {
				return err
			}

			reminderConfig, ok := stateStore.GetAppliedReminder(name)
			if !ok {
				return notAppliedErr(kindReminder, name)
			}

			if reminderConfig.Check != "" {
				checkCmd := exec.Command("sh", "-c", reminderConfig.Check)
				checkErr := checkCmd.Run()

				rs := stateStore.GetReminderState(name)
				exitCode := 0
				if checkErr != nil {
					exitCode = 1
					var exitError *exec.ExitError
					if errors.As(checkErr, &exitError) {
						exitCode = exitError.ExitCode()
					}
				}
				rs.LastCheckExitCode = &exitCode
				now := time.Now()
				rs.LastCheckAt = now
				stateStore.SetReminderState(name, rs)

				if checkErr != nil {
					if saveErr := stateStore.Save(); saveErr != nil {
						return fmt.Errorf("saving check state: %w", saveErr)
					}
					return nil
				}
			}

			if err := sendNotification(name, reminderConfig.Message); err != nil {
				fmt.Fprintf(os.Stderr, "[ORBIT] Warning: failed to send notification: %v\n", err)
			}

			h := reminder.NewHandler(stateStore)
			if err := h.Fire(name); err != nil {
				return fmt.Errorf("saving state: %w", err)
			}

			manager := systemd.NewManager(resolveOrbitBinary())
			removeSnoozeTimer(manager, name)

			return nil
		},
	}
}

// withConfigFlag adds the --config flag to a command. Only commands that read
// orbit.toml need this (apply, plan, edit, doctor).
func withConfigFlag(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().String("config", "", "path to config file (default: $XDG_CONFIG_HOME/orbit/orbit.toml)")
	//nolint:errcheck
	cmd.MarkFlagFilename("config", "toml")
	return cmd
}

// getConfigPathFromCmd returns the config path from --config flag or the default.
func getConfigPathFromCmd(cmd *cobra.Command) (string, error) {
	if v, err := cmd.Flags().GetString("config"); err == nil && v != "" {
		return v, nil
	}
	dir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "orbit.toml"), nil
}

// loadConfig loads and validates the configuration.
func loadConfig(cmd *cobra.Command) (*config.Config, error) {
	configPath, err := getConfigPathFromCmd(cmd)
	if err != nil {
		return nil, err
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading configuration: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

// newState initializes the state store.
func newState() (*state.State, error) {
	dataDir, err := getDataDir()
	if err != nil {
		return nil, err
	}
	stateStore, err := state.NewState(dataDir)
	if err != nil {
		return nil, fmt.Errorf("initializing state store: %w", err)
	}
	return stateStore, nil
}

// nameExtractor extracts a list of names from an applied config.
type nameExtractor func(*state.AppliedConfig) []string

// taskNames extracts task names from an applied config for use with pickName.
func taskNames(applied *state.AppliedConfig) []string {
	if applied == nil {
		return nil
	}
	names := make([]string, 0, len(applied.Tasks))
	for name := range applied.Tasks {
		names = append(names, name)
	}
	return names
}

// reminderNames extracts reminder names from an applied config for use with pickName.
func reminderNames(applied *state.AppliedConfig) []string {
	if applied == nil {
		return nil
	}
	names := make([]string, 0, len(applied.Reminders))
	for name := range applied.Reminders {
		names = append(names, name)
	}
	return names
}

// pickName prompts the user to select a name if no argument is given.
func pickName(args []string, prompt string, stateStore *state.State, kind entryKind, extract nameExtractor) (string, error) {
	if stateStore == nil {
		var err error
		stateStore, err = newState()
		if err != nil {
			return "", err
		}
	}
	applied := stateStore.GetAppliedConfig()
	names := extract(applied)
	if len(names) == 0 {
		return "", fmt.Errorf("no %ss configured (run 'orbit apply' first)", kind)
	}

	if len(args) > 0 {
		if !slices.Contains(names, args[0]) {
			return "", notAppliedErr(kind, args[0])
		}
		return args[0], nil
	}

	sortNatural(names)
	return picker.Pick(prompt, names)
}

// completeNames returns a cobra completion function for the given name extractor.
func completeNames(extract nameExtractor) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		stateStore, err := newState()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		applied := stateStore.GetAppliedConfig()
		if applied == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names := extract(applied)
		sortNatural(names)
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// getConfigDir returns the orbit config directory, respecting XDG_CONFIG_HOME.
func getConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "orbit"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "orbit"), nil
}

// getDataDir returns the orbit data directory, respecting XDG_DATA_HOME.
func getDataDir() (string, error) {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "orbit"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "orbit"), nil
}

// currentEmbedVersion should be bumped whenever embedded assets (icon.png, schema.json) change.
const currentEmbedVersion = 1

// ensureEmbeddedAssets writes embedded assets to disk if the stored version is
// outdated or if any files are missing.
func ensureEmbeddedAssets(stateStore *state.State) {
	dataDir, err := getDataDir()
	if err != nil {
		return
	}
	configDir, err := getConfigDir()
	if err != nil {
		return
	}

	iconPath := filepath.Join(dataDir, "icon.png")
	schemaPath := filepath.Join(configDir, "schema.json")

	assets := []struct {
		path string
		data []byte
	}{
		{iconPath, iconPNG},
		{schemaPath, schemaJSON},
	}

	needsWrite := stateStore.GetEmbedVersion() < currentEmbedVersion
	if !needsWrite {
		// Version is current — only write if files are missing
		for _, a := range assets {
			if _, err := os.Stat(a.path); os.IsNotExist(err) {
				needsWrite = true
				break
			}
		}
	}

	if !needsWrite {
		return
	}

	allOk := true
	for _, a := range assets {
		_ = os.MkdirAll(filepath.Dir(a.path), 0755)
		if err := os.WriteFile(a.path, a.data, 0644); err != nil {
			allOk = false
		}
	}

	// Only bump the version if all writes succeeded, so we retry on next run
	if allOk && stateStore.GetEmbedVersion() < currentEmbedVersion {
		stateStore.SetEmbedVersion(currentEmbedVersion)
		_ = stateStore.Save()
	}
}

//go:embed icon.png
var iconPNG []byte

// sendNotification sends a desktop notification via notify-send.
func sendNotification(name, message string) error {
	title := fmt.Sprintf("Orbit: %s", name)
	dataDir, err := getDataDir()
	if err != nil {
		return err
	}
	iconPath := filepath.Join(dataDir, "icon.png")
	return exec.Command("notify-send", "--icon", iconPath, title, message).Run()
}

// resolveOrbitBinary returns the absolute path of the running orbit binary.
// Falls back to "orbit" with a warning if the path cannot be determined.
func resolveOrbitBinary() string {
	if path, err := os.Executable(); err == nil {
		return path
	}
	fmt.Fprintf(os.Stderr, "%s could not determine orbit binary path; using bare \"orbit\" in unit files.\n", yellow("Warning:"))
	fmt.Fprintln(os.Stderr, "Ensure \"orbit\" is in systemd's PATH or re-run with an absolute path.")
	return "orbit"
}

func notAppliedErr(kind entryKind, name string) error {
	return fmt.Errorf("%s '%s' not found in applied config (run 'orbit apply' first)", kind, name)
}

func removeSnoozeTimer(manager *systemd.Manager, name string) {
	if err := manager.RemoveUnits([]systemd.Unit{{Name: systemd.SnoozeTimerName(name)}}); err != nil {
		fmt.Fprintf(os.Stderr, "%s failed to remove snooze timer for '%s': %v\n", yellow("Warning:"), name, err)
	}
}

// toAppliedConfig converts a user config to an AppliedConfig for storage in state.
func toAppliedConfig(cfg *config.Config) *state.AppliedConfig {
	ac := &state.AppliedConfig{
		Tasks:     make(map[string]state.AppliedTaskConfig, len(cfg.Tasks)),
		Reminders: make(map[string]state.AppliedReminderConfig, len(cfg.Reminders)),
	}
	for name, t := range cfg.Tasks {
		ac.Tasks[name] = state.AppliedTaskConfig{
			Command:  t.Command,
			Schedule: t.Schedule,
			OnMissed: t.OnMissed,
			Retry: state.AppliedRetryConfig{
				Attempts: t.Retry.GetAttempts(),
				Delay:    t.Retry.Delay,
			},
		}
	}
	for name, r := range cfg.Reminders {
		ac.Reminders[name] = state.AppliedReminderConfig{
			Command:  r.Command,
			Schedule: r.Schedule,
			Message:  r.Message,
			Snooze:   r.Snooze,
			Check:    r.Check,
		}
	}
	return ac
}

// plural returns "s" for counts other than 1.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// joinAnd joins up to two strings with "and" (e.g. "3 tasks and 2 reminders").
func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return parts[0] + " and " + parts[1]
	}
}

// naturalLess compares two strings with natural (alphanumeric) ordering.
// "task2" < "task10" because 2 < 10 numerically.
func naturalLess(a, b string) bool {
	for {
		if a == b {
			return false
		}
		if a == "" {
			return true
		}
		if b == "" {
			return false
		}

		// Split off leading chunk (all-digit or all-non-digit)
		aChunk, aRest := splitChunk(a)
		bChunk, bRest := splitChunk(b)

		aDigit := aChunk[0] >= '0' && aChunk[0] <= '9'
		bDigit := bChunk[0] >= '0' && bChunk[0] <= '9'

		var cmp int
		if aDigit && bDigit {
			cmp = compareNumeric(aChunk, bChunk)
		} else {
			if aChunk < bChunk {
				cmp = -1
			} else if aChunk > bChunk {
				cmp = 1
			}
		}

		if cmp != 0 {
			return cmp < 0
		}
		a = aRest
		b = bRest
	}
}

// splitChunk splits s into a leading run of digits or non-digits and the remainder.
func splitChunk(s string) (chunk, rest string) {
	if s == "" {
		return "", ""
	}
	isDigit := s[0] >= '0' && s[0] <= '9'
	i := 1
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') == isDigit {
		i++
	}
	return s[:i], s[i:]
}

// compareNumeric compares two digit-only strings numerically, returning -1, 0, or 1.
func compareNumeric(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// sortNatural sorts strings using natural alphanumeric ordering ("task2" < "task10").
func sortNatural(s []string) {
	sort.Slice(s, func(i, j int) bool {
		return naturalLess(s[i], s[j])
	})
}
