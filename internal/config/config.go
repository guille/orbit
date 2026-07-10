package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// IncludeResolution records how one include pattern resolved.
type IncludeResolution struct {
	Pattern  string   // as written in the config file, including any leading '?'
	Optional bool     // pattern had a leading '?'
	IsGlob   bool     // stripped pattern contains '*'
	Files    []string // absolute cleaned paths actually loaded, in load order
}

// Config represents the orbit configuration structure.
type Config struct {
	OrbitBin  string                    `toml:"orbit_bin"`
	Include   []string                  `toml:"include"`
	Tasks     map[string]TaskConfig     `toml:"tasks"`
	Reminders map[string]ReminderConfig `toml:"reminders"`

	// IncludeResolutions is populated by LoadConfig, one entry per Include
	// pattern, in order.
	IncludeResolutions []IncludeResolution `toml:"-"`

	// sources maps "tasks/<name>" and "reminders/<name>" to the absolute
	// path of the file that defined the entry.
	sources map[string]string

	// rootPath is the absolute path of the root config file, used to
	// distinguish root entries from included-file entries in error messages.
	rootPath string
}

// Source returns the file that defined the given entry ("" if unknown).
// section is "tasks" or "reminders".
func (c *Config) Source(section, name string) string {
	return c.sources[section+"/"+name]
}

// sourceSuffix annotates errors for entries that came from an included file.
// Returns "" for root-file entries so single-file error messages are unchanged.
func (c *Config) sourceSuffix(section, name string) string {
	src := c.Source(section, name)
	if src == "" || src == c.rootPath {
		return ""
	}
	return fmt.Sprintf(" (defined in %s)", src)
}

// OnMissedPolicy defines behavior when a scheduled run is missed.
type OnMissedPolicy string

const (
	OnMissedRunOnce OnMissedPolicy = "run_once"
	OnMissedSkip    OnMissedPolicy = "skip"
)

// TaskConfig represents a task configuration.
type TaskConfig struct {
	Command  string         `toml:"command"`
	Schedule string         `toml:"schedule"`
	OnMissed OnMissedPolicy `toml:"on_missed"`
	Retry    RetryConfig    `toml:"retry"`
}

// RetryConfig represents retry settings for a task.
type RetryConfig struct {
	Attempts *int   `toml:"attempts"`
	Delay    string `toml:"delay"`
}

// GetAttempts returns the number of retry attempts, defaulting to 0 if nil.
func (r RetryConfig) GetAttempts() int {
	if r.Attempts == nil {
		return 0
	}
	return *r.Attempts
}

// ReminderConfig represents a reminder configuration.
type ReminderConfig struct {
	Command  string `toml:"command"` // optional
	Schedule string `toml:"schedule"`
	Message  string `toml:"message"`
	Snooze   string `toml:"snooze"`
	Check    string `toml:"check"` // optional: only fire if this command exits 0
}

// LoadConfig loads and returns the configuration from the specified path.
func LoadConfig(path string) (*Config, error) {
	absRoot, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving config path: %w", err)
	}

	data, err := os.ReadFile(absRoot)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}

	cfg.rootPath = absRoot
	cfg.sources = make(map[string]string)
	if cfg.Tasks == nil {
		cfg.Tasks = make(map[string]TaskConfig)
	}
	if cfg.Reminders == nil {
		cfg.Reminders = make(map[string]ReminderConfig)
	}
	for name := range cfg.Tasks {
		cfg.sources["tasks/"+name] = absRoot
	}
	for name := range cfg.Reminders {
		cfg.sources["reminders/"+name] = absRoot
	}

	if err := cfg.resolveIncludes(); err != nil {
		return nil, err
	}

	cfg.applyDefaults() // after merging, once, on the full config
	return &cfg, nil
}

// resolveIncludes loads every file named by c.Include and merges its
// entries into c. Must be called after c.rootPath/c.sources are set and
// before applyDefaults.
func (c *Config) resolveIncludes() error {
	if len(c.Include) == 0 {
		return nil
	}

	rootDir := filepath.Dir(c.rootPath)
	seen := map[string]bool{filepath.Clean(c.rootPath): true}

	for i, raw := range c.Include {
		pattern, optional := strings.CutPrefix(raw, "?")
		if pattern == "" {
			return fmt.Errorf("include: empty pattern at index %d", i)
		}

		expanded, err := expandTilde(pattern)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(expanded) {
			expanded = filepath.Join(rootDir, expanded)
		}

		res := IncludeResolution{Pattern: raw, Optional: optional, IsGlob: isGlobPattern(pattern)}

		var files []string
		if res.IsGlob {
			files, err = filepath.Glob(escapeNonStarMeta(expanded))
			if err != nil { // ErrBadPattern; unreachable after escaping, kept defensively
				return fmt.Errorf("include %q: invalid glob pattern", raw)
			}
		} else {
			info, err := os.Stat(expanded)
			switch {
			case os.IsNotExist(err) && optional:
				// optional include not present on this machine: load nothing
			case os.IsNotExist(err):
				return fmt.Errorf("include %q: file not found (resolved to %s)", raw, expanded)
			case err != nil:
				return fmt.Errorf("include %q: %w", raw, err)
			case info.IsDir():
				return fmt.Errorf("include %q: %s is a directory, not a file", raw, expanded)
			default:
				files = []string{expanded}
			}
		}

		for _, f := range files {
			f = filepath.Clean(f)
			if seen[f] {
				continue // dedup; also excludes the root file itself
			}
			if info, err := os.Stat(f); err == nil && info.IsDir() {
				continue // glob matched a directory
			}
			seen[f] = true
			if err := c.mergeIncludedFile(f); err != nil {
				return err
			}
			res.Files = append(res.Files, f)
		}

		c.IncludeResolutions = append(c.IncludeResolutions, res)
	}
	return nil
}

// mergeIncludedFile reads, parses, and merges a single included TOML file.
func (c *Config) mergeIncludedFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("include %s: %w", path, err)
	}

	var inc Config
	if err := toml.Unmarshal(data, &inc); err != nil {
		return fmt.Errorf("included file %s: invalid TOML: %w", path, err)
	}
	if len(inc.Include) > 0 {
		return fmt.Errorf("included file %s: nested includes are not supported", path)
	}
	if inc.OrbitBin != "" {
		return fmt.Errorf("included file %s: orbit_bin may only be set in the root config", path)
	}

	for name, t := range inc.Tasks {
		if prev, ok := c.sources["tasks/"+name]; ok {
			return fmt.Errorf("task %q: defined in both %s and %s", name, prev, path)
		}
		c.Tasks[name] = t
		c.sources["tasks/"+name] = path
	}
	for name, r := range inc.Reminders {
		if prev, ok := c.sources["reminders/"+name]; ok {
			return fmt.Errorf("reminder %q: defined in both %s and %s", name, prev, path)
		}
		c.Reminders[name] = r
		c.sources["reminders/"+name] = path
	}
	return nil
}

// isGlobPattern reports whether p uses the '*' wildcard.
func isGlobPattern(p string) bool { return strings.Contains(p, "*") }

// escapeNonStarMeta backslash-escapes filepath.Match metacharacters other
// than '*': '?', '[', // and '\' in patterns match themselves literally.
func escapeNonStarMeta(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r == '?' || r == '[' || r == ']' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// expandTilde replaces a leading '~' or '~/' with the user home directory.
func expandTilde(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("include %q: resolving home directory: %w", p, err)
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	}
	if strings.HasPrefix(p, "~") {
		return "", fmt.Errorf("include %q: '~user' expansion is not supported", p)
	}
	return p, nil
}

// applyDefaults fills in default values for optional fields.
func (c *Config) applyDefaults() {
	if c.OrbitBin == "" {
		c.OrbitBin = "orbit"
	}
	for name, t := range c.Tasks {
		if t.OnMissed == "" && t.Schedule != "" {
			t.OnMissed = OnMissedRunOnce
		}
		if t.Retry.Attempts == nil {
			t.Retry.Attempts = new(3)
		}
		if t.Retry.Delay == "" {
			t.Retry.Delay = "5m"
		}
		c.Tasks[name] = t
	}

	for name, r := range c.Reminders {
		if r.Snooze == "" {
			r.Snooze = "2h"
		}
		c.Reminders[name] = r
	}
}

// validName matches names safe for systemd unit filenames and ExecStart arguments.
var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// Validate checks the configuration for required fields and valid values.
func (c *Config) Validate() error {
	for name, t := range c.Tasks {
		if !validName.MatchString(name) {
			return fmt.Errorf("task %q: name must match [a-zA-Z0-9][a-zA-Z0-9_-]* (letters, digits, hyphens, underscores; must start with letter or digit)", name)
		}
		if t.Command == "" {
			return fmt.Errorf("task %s: command is required%s", name, c.sourceSuffix("tasks", name))
		}
		if t.Schedule != "" {
			switch t.OnMissed {
			case OnMissedRunOnce, OnMissedSkip:
				// valid
			default:
				return fmt.Errorf("task %s: on_missed must be one of: run_once, skip%s", name, c.sourceSuffix("tasks", name))
			}
		} else if t.OnMissed != "" {
			return fmt.Errorf("task %s: on_missed is only valid with a schedule%s", name, c.sourceSuffix("tasks", name))
		}
		if t.Retry.Attempts != nil && *t.Retry.Attempts < 0 {
			return fmt.Errorf("task %s: retry.attempts must be non-negative%s", name, c.sourceSuffix("tasks", name))
		}
		if t.Retry.Delay != "" {
			if _, err := time.ParseDuration(t.Retry.Delay); err != nil {
				return fmt.Errorf("task %s: invalid retry.delay %q: %v%s", name, t.Retry.Delay, err, c.sourceSuffix("tasks", name))
			}
		}
	}

	for name, r := range c.Reminders {
		if !validName.MatchString(name) {
			return fmt.Errorf("reminder %q: name must match [a-zA-Z0-9][a-zA-Z0-9_-]* (letters, digits, hyphens, underscores; must start with letter or digit)", name)
		}
		if r.Schedule == "" {
			return fmt.Errorf("reminder %s: schedule is required%s", name, c.sourceSuffix("reminders", name))
		}
		if r.Message == "" {
			return fmt.Errorf("reminder %s: message is required%s", name, c.sourceSuffix("reminders", name))
		}
		if r.Snooze != "" {
			if _, err := time.ParseDuration(r.Snooze); err != nil {
				return fmt.Errorf("reminder %s: invalid snooze duration %q: %v%s", name, r.Snooze, err, c.sourceSuffix("reminders", name))
			}
		}
	}

	// Validate schedule expressions via systemd-analyze calendar (once per unique schedule).
	schedules := make(map[string]string) // schedule -> first user (for error messages)
	for name, t := range c.Tasks {
		if t.Schedule != "" {
			if _, exists := schedules[t.Schedule]; !exists {
				schedules[t.Schedule] = "task " + name + c.sourceSuffix("tasks", name)
			}
		}
	}
	for name, r := range c.Reminders {
		if _, exists := schedules[r.Schedule]; !exists {
			schedules[r.Schedule] = "reminder " + name + c.sourceSuffix("reminders", name)
		}
	}
	for schedule, user := range schedules {
		if err := validateSchedule(schedule); err != nil {
			return fmt.Errorf("%s: invalid schedule %q: %v", user, schedule, err)
		}
	}

	return nil
}

// validateSchedule checks a schedule expression by calling systemd-analyze calendar.
func validateSchedule(schedule string) error {
	cmd := exec.Command("systemd-analyze", "calendar", schedule)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemd-analyze rejected expression (%s)", firstLine(output))
	}
	return nil
}

func firstLine(b []byte) string {
	for i, c := range b {
		if c == '\n' {
			return string(b[:i])
		}
	}
	return string(b)
}
