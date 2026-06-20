package config

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"time"

	"github.com/BurntSushi/toml"
)

// Config represents the orbit configuration structure.
type Config struct {
	OrbitBin  string                    `toml:"orbit_bin"`
	Tasks     map[string]TaskConfig     `toml:"tasks"`
	Reminders map[string]ReminderConfig `toml:"reminders"`
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}

	cfg.applyDefaults()
	return &cfg, nil
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
			return fmt.Errorf("task %s: command is required", name)
		}
		if t.Schedule != "" {
			switch t.OnMissed {
			case OnMissedRunOnce, OnMissedSkip:
				// valid
			default:
				return fmt.Errorf("task %s: on_missed must be one of: run_once, skip", name)
			}
		} else if t.OnMissed != "" {
			return fmt.Errorf("task %s: on_missed is only valid with a schedule", name)
		}
		if t.Retry.Attempts != nil && *t.Retry.Attempts < 0 {
			return fmt.Errorf("task %s: retry.attempts must be non-negative", name)
		}
		if t.Retry.Delay != "" {
			if _, err := time.ParseDuration(t.Retry.Delay); err != nil {
				return fmt.Errorf("task %s: invalid retry.delay %q: %v", name, t.Retry.Delay, err)
			}
		}
	}

	for name, r := range c.Reminders {
		if !validName.MatchString(name) {
			return fmt.Errorf("reminder %q: name must match [a-zA-Z0-9][a-zA-Z0-9_-]* (letters, digits, hyphens, underscores; must start with letter or digit)", name)
		}
		if r.Schedule == "" {
			return fmt.Errorf("reminder %s: schedule is required", name)
		}
		if r.Message == "" {
			return fmt.Errorf("reminder %s: message is required", name)
		}
		if r.Snooze != "" {
			if _, err := time.ParseDuration(r.Snooze); err != nil {
				return fmt.Errorf("reminder %s: invalid snooze duration %q: %v", name, r.Snooze, err)
			}
		}
	}

	// Validate schedule expressions via systemd-analyze calendar (once per unique schedule).
	schedules := make(map[string]string) // schedule -> first user (for error messages)
	for name, t := range c.Tasks {
		if t.Schedule != "" {
			if _, exists := schedules[t.Schedule]; !exists {
				schedules[t.Schedule] = "task " + name
			}
		}
	}
	for name, r := range c.Reminders {
		if _, exists := schedules[r.Schedule]; !exists {
			schedules[r.Schedule] = "reminder " + name
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
