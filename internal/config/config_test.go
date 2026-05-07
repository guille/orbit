package config

import (
	"fmt"
	"os"
	"testing"

	"github.com/BurntSushi/toml"
)

// loadFromString is a test helper that parses TOML and applies defaults,
// matching the behavior of LoadConfig but without needing a file.
func loadFromString(data string) (*Config, error) {
	var cfg Config
	if err := toml.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	return &cfg, nil
}

func TestLoadConfig_Valid(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.backup]
command        = "rsync -av ~/Documents /mnt/backup/"
schedule       = "daily"
on_missed      = "run_once"
retry.attempts = 3
retry.delay    = "5m"

[tasks.deploy]
command  = "./deploy.sh"
schedule = "weekly"

[reminders.weekly-review]
command  = "echo review"
schedule = "weekly"
message  = "Time for your weekly review"
snooze   = "2h"
`)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if len(cfg.Tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(cfg.Tasks))
	}

	backup := cfg.Tasks["backup"]
	assertEqual(t, "backup.Command", backup.Command, "rsync -av ~/Documents /mnt/backup/")
	assertEqual(t, "backup.Schedule", backup.Schedule, "daily")
	assertEqual(t, "backup.OnMissed", string(backup.OnMissed), "run_once")
	assertEqualInt(t, "backup.Retry.Attempts", backup.Retry.GetAttempts(), 3)
	assertEqual(t, "backup.Retry.Delay", backup.Retry.Delay, "5m")

	deploy := cfg.Tasks["deploy"]
	assertEqual(t, "deploy.Command", deploy.Command, "./deploy.sh")
	// Defaults applied
	assertEqual(t, "deploy.OnMissed", string(deploy.OnMissed), "run_once")
	assertEqualInt(t, "deploy.Retry.Attempts", deploy.Retry.GetAttempts(), 3)
	assertEqual(t, "deploy.Retry.Delay", deploy.Retry.Delay, "5m")

	if len(cfg.Reminders) != 1 {
		t.Fatalf("Expected 1 reminder, got %d", len(cfg.Reminders))
	}

	wr := cfg.Reminders["weekly-review"]
	assertEqual(t, "weekly-review.Command", wr.Command, "echo review")
	assertEqual(t, "weekly-review.Schedule", wr.Schedule, "weekly")
	assertEqual(t, "weekly-review.Message", wr.Message, "Time for your weekly review")
	assertEqual(t, "weekly-review.Snooze", wr.Snooze, "2h")
}

func TestLoadConfig_WithDefaults(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.minimal]
command  = "echo hello"
schedule = "@daily"

[reminders.minimal]
schedule = "@hourly"
message  = "Hello"
`)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	task := cfg.Tasks["minimal"]
	assertEqual(t, "OnMissed", string(task.OnMissed), "run_once")
	assertEqualInt(t, "Retry.Attempts", task.Retry.GetAttempts(), 3)
	assertEqual(t, "Retry.Delay", task.Retry.Delay, "5m")

	reminder := cfg.Reminders["minimal"]
	assertEqual(t, "Snooze", reminder.Snooze, "2h")
	assertEqual(t, "Command", reminder.Command, "") // optional, empty is fine
}

func TestValidate_Valid(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.valid]
command    = "echo test"
schedule   = "daily"
on_missed  = "run_once"

[reminders.valid]
schedule = "hourly"
message  = "Test reminder"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Expected valid config, got error: %v", err)
	}
}

func TestValidate_MissingTaskCommand(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.invalid]
schedule = "daily"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Expected validation error for missing command")
	}
}

func TestValidate_TaskWithoutSchedule(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.manual]
command = "echo test"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Task without schedule should be valid, got: %v", err)
	}
	// on_missed should not be defaulted for unscheduled tasks
	if cfg.Tasks["manual"].OnMissed != "" {
		t.Fatalf("Expected empty on_missed for unscheduled task, got %q", cfg.Tasks["manual"].OnMissed)
	}
}

func TestValidate_OnMissedWithoutSchedule(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.invalid]
command   = "echo test"
on_missed = "run_once"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Expected validation error for on_missed without schedule")
	}
}

func TestValidate_InvalidOnMissed(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.invalid]
command   = "echo test"
schedule  = "daily"
on_missed = "bogus"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Expected validation error for invalid on_missed")
	}
}

func TestValidate_MissingReminderSchedule(t *testing.T) {
	cfg, err := loadFromString(`
[reminders.invalid]
message = "Test"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Expected validation error for missing reminder schedule")
	}
}

func TestValidate_MissingReminderMessage(t *testing.T) {
	cfg, err := loadFromString(`
[reminders.invalid]
schedule = "hourly"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Expected validation error for missing reminder message")
	}
}

func TestLoadConfig_ExplicitZeroRetryAttempts(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.noretry]
command        = "echo hello"
schedule       = "daily"
retry.attempts = 0
`)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	task := cfg.Tasks["noretry"]
	if task.Retry.Attempts == nil {
		t.Fatal("Expected Attempts to be non-nil (explicitly set to 0)")
	}
	assertEqualInt(t, "Retry.Attempts", task.Retry.GetAttempts(), 0)
}

func TestValidate_EmptyConfig(t *testing.T) {
	cfg, err := loadFromString(``)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Empty config should be valid, got: %v", err)
	}
}

func TestValidate_InvalidTaskName(t *testing.T) {
	tests := []struct {
		name string
		toml string
	}{
		{"spaces", `[tasks."has space"]` + "\ncommand = \"echo\"\nschedule = \"daily\""},
		{"slash", `[tasks."with/slash"]` + "\ncommand = \"echo\"\nschedule = \"daily\""},
		{"dot", `[tasks."with.dot"]` + "\ncommand = \"echo\"\nschedule = \"daily\""},
		{"starts with dash", `[tasks."-start"]` + "\ncommand = \"echo\"\nschedule = \"daily\""},
	}

	for _, tc := range tests {
		cfg, err := loadFromString(tc.toml)
		if err != nil {
			t.Fatalf("%s: load error: %v", tc.name, err)
		}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s: expected validation error for invalid name", tc.name)
		}
	}
}

func TestValidate_ValidTaskNames(t *testing.T) {
	names := []string{"backup", "my-task", "task_1", "A123", "a"}
	for _, name := range names {
		toml := fmt.Sprintf("[tasks.%s]\ncommand = \"echo\"\nschedule = \"daily\"\n", name)
		cfg, err := loadFromString(toml)
		if err != nil {
			t.Fatalf("%s: load error: %v", name, err)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s: expected valid, got: %v", name, err)
		}
	}
}

func TestValidate_InvalidRetryDelay(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.test]
command        = "echo test"
schedule       = "daily"
retry.delay    = "5 minutes"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Expected validation error for invalid retry.delay")
	}
}

func TestValidate_InvalidSnoozeDuration(t *testing.T) {
	cfg, err := loadFromString(`
[reminders.test]
schedule = "daily"
message  = "Test"
snooze   = "2hours"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Expected validation error for invalid snooze duration")
	}
}

func TestValidate_ReminderWithCheck(t *testing.T) {
	cfg, err := loadFromString(`
[reminders.check-pacnew]
schedule = "daily"
check    = "locate .pacnew"
message  = "There are pacnew files to review!"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Expected valid config with check field, got: %v", err)
	}
	if cfg.Reminders["check-pacnew"].Check != "locate .pacnew" {
		t.Fatalf("Expected check 'locate .pacnew', got %q", cfg.Reminders["check-pacnew"].Check)
	}
}

// helpers
func assertEqual(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: expected %q, got %q", field, want, got)
	}
}

func assertEqualInt(t *testing.T, field string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: expected %d, got %d", field, want, got)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path/orbit.toml")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestLoadConfig_InvalidTOML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/orbit.toml"
	if err := os.WriteFile(path, []byte("this is not [valid toml =\""), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid TOML")
	}
}

func TestValidate_NegativeRetryAttempts(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.test]
command        = "echo test"
schedule       = "daily"
retry.attempts = -1
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Expected validation error for negative retry.attempts")
	}
}
