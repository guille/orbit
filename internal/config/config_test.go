package config

import (
	"fmt"
	"os"
	"strings"
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

func TestValidate_InvalidSchedule(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.good]
command  = "echo test"
schedule = "daily"

[tasks.bad]
command  = "echo test"
schedule = "evry day"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Expected validation error for invalid schedule")
	}
	if !strings.Contains(err.Error(), "evry day") {
		t.Errorf("error should name the offending expression, got: %v", err)
	}
}

// A schedule can be well-formed yet have no future trigger, so validation must
// key off parseability rather than whether a next elapse exists.
func TestValidate_ScheduleThatNeverElapses(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.past]
command  = "echo test"
schedule = "2020-01-01 00:00:00"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Expected past-only schedule to be valid, got error: %v", err)
	}
}

// Several bad schedules must always produce the same complaint.
func TestValidate_InvalidScheduleIsDeterministic(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.a]
command  = "echo test"
schedule = "zzz bad"

[tasks.b]
command  = "echo test"
schedule = "aaa bad"

[tasks.c]
command  = "echo test"
schedule = "mmm bad"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	first := cfg.Validate()
	if first == nil {
		t.Fatal("Expected validation error for invalid schedules")
	}
	for range 5 {
		if got := cfg.Validate(); got.Error() != first.Error() {
			t.Fatalf("non-deterministic error:\n  %v\n  %v", first, got)
		}
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

// writeFile creates a file with the given content in dir, returning its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := dir + "/" + name
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeRoot writes the root orbit.toml and returns its absolute path.
func writeRoot(t *testing.T, dir, content string) string {
	t.Helper()
	return writeFile(t, dir, "orbit.toml", content)
}

// TestInclude_NoIncludeKey: case 1 — no include key behaves like today.
func TestInclude_NoIncludeKey(t *testing.T) {
	dir := t.TempDir()
	root := writeRoot(t, dir, `
[tasks.backup]
command = "rsync"
`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Tasks) != 1 || cfg.Tasks["backup"].Command != "rsync" {
		t.Fatal("expected backup task")
	}
	if len(cfg.IncludeResolutions) != 0 {
		t.Fatalf("expected no resolutions, got %d", len(cfg.IncludeResolutions))
	}
}

// TestInclude_EmptyArray: case 2 — include = [] same as no include.
func TestInclude_EmptyArray(t *testing.T) {
	dir := t.TempDir()
	root := writeRoot(t, dir, `include = []
[tasks.backup]
command = "rsync"
`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(cfg.Tasks))
	}
	if len(cfg.IncludeResolutions) != 0 {
		t.Fatalf("expected no resolutions, got %d", len(cfg.IncludeResolutions))
	}
}

// TestInclude_LiteralMerge: case 3 — literal include merges tasks/reminders; Source() works.
func TestInclude_LiteralMerge(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "extra.toml", `
[tasks.extra-task]
command = "echo extra"

[reminders.extra-rem]
schedule = "daily"
message  = "hello"
`)
	root := writeRoot(t, dir, `
include = ["extra.toml"]
[tasks.root-task]
command = "echo root"
`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(cfg.Tasks))
	}

	// Source() for root entry points to root file.
	rootSrc := cfg.Source("tasks", "root-task")
	if rootSrc != root {
		t.Errorf("root-task source: got %q, want %q", rootSrc, root)
	}
	// Source() for included entry points to included file.
	extraSrc := cfg.Source("tasks", "extra-task")
	if extraSrc == "" || extraSrc == root {
		t.Errorf("extra-task source should be the included file, got %q", extraSrc)
	}
	if cfg.Source("reminders", "extra-rem") != extraSrc {
		t.Errorf("extra-rem source should match included file")
	}
}

// TestInclude_LiteralMissing: case 4 — missing literal → error with "file not found".
func TestInclude_LiteralMissing(t *testing.T) {
	dir := t.TempDir()
	root := writeRoot(t, dir, `include = ["nonexistent.toml"]`)
	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("expected error for missing literal include")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("error should mention 'file not found': %v", err)
	}
	// Should contain the resolved absolute path.
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error should contain resolved path (dir=%s): %v", dir, err)
	}
}

// TestInclude_LiteralIsDir: case 5 — literal pointing at a directory → error.
func TestInclude_LiteralIsDir(t *testing.T) {
	dir := t.TempDir()
	subdir := dir + "/subdir"
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	root := writeRoot(t, dir, `include = ["subdir"]`)
	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("expected error for directory include")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("error should mention directory: %v", err)
	}
}

// TestInclude_GlobMatchesTwo: case 6 — glob matching two files; both merged in sorted order.
func TestInclude_GlobMatchesTwo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.toml", `[tasks.a-task]
command = "echo a"
`)
	writeFile(t, dir, "b.toml", `[tasks.b-task]
command = "echo b"
`)
	root := writeRoot(t, dir, `include = ["*.toml"]`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.IncludeResolutions) != 1 {
		t.Fatalf("expected 1 resolution, got %d", len(cfg.IncludeResolutions))
	}
	res := cfg.IncludeResolutions[0]
	if len(res.Files) != 2 {
		t.Fatalf("expected 2 files loaded, got %d: %v", len(res.Files), res.Files)
	}
	if !res.IsGlob {
		t.Error("expected IsGlob=true")
	}
	// Files should be in sorted (lexical) order.
	if !strings.Contains(res.Files[0], "a.toml") || !strings.Contains(res.Files[1], "b.toml") {
		t.Errorf("files not in sorted order: %v", res.Files)
	}
	if len(cfg.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(cfg.Tasks))
	}
}

// TestInclude_GlobMatchesZero: case 7 — glob with zero matches; no error, Files is nil/empty.
func TestInclude_GlobMatchesZero(t *testing.T) {
	dir := t.TempDir()
	root := writeRoot(t, dir, `include = ["orbit.d/*.toml"]`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.IncludeResolutions) != 1 {
		t.Fatalf("expected 1 resolution, got %d", len(cfg.IncludeResolutions))
	}
	res := cfg.IncludeResolutions[0]
	if len(res.Files) != 0 {
		t.Errorf("expected 0 files, got %v", res.Files)
	}
	if !res.IsGlob {
		t.Error("expected IsGlob=true")
	}
}

// TestInclude_GlobSelfExclusion: case 8 — *.toml in config dir doesn't reload root.
func TestInclude_GlobSelfExclusion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "extra.toml", `[tasks.extra]
command = "echo extra"
`)
	// Root defines root-task; if root were reloaded, duplicate error would occur.
	root := writeRoot(t, dir, `include = ["*.toml"]
[tasks.root-task]
command = "echo root"
`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("should not error (root should be excluded from glob): %v", err)
	}
	// Only extra-task from extra.toml, not a duplicate root-task.
	if len(cfg.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(cfg.Tasks))
	}
}

// TestInclude_DeduplicateLiteralAndGlob: case 9 — same file via literal and glob → loaded once.
func TestInclude_DeduplicateLiteralAndGlob(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "extra.toml", `[tasks.extra-task]
command = "echo extra"
`)
	root := writeRoot(t, dir, `include = ["extra.toml", "*.toml"]`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Tasks) != 1 {
		t.Fatalf("expected 1 task (deduped), got %d", len(cfg.Tasks))
	}
}

// TestInclude_DuplicateRootVsInclude: case 10 — duplicate task name root vs include → error.
func TestInclude_DuplicateRootVsInclude(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "extra.toml", `[tasks.shared]
command = "echo extra"
`)
	root := writeRoot(t, dir, `include = ["extra.toml"]
[tasks.shared]
command = "echo root"
`)
	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("expected duplicate task error")
	}
	if !strings.Contains(err.Error(), "task") || !strings.Contains(err.Error(), "shared") {
		t.Errorf("error should name the task: %v", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error should name both file paths: %v", err)
	}
}

// TestInclude_DuplicateAcrossIncludes: case 11 — duplicate task across two included files.
func TestInclude_DuplicateAcrossIncludes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.toml", `[tasks.shared]
command = "echo a"
`)
	writeFile(t, dir, "b.toml", `[tasks.shared]
command = "echo b"
`)
	root := writeRoot(t, dir, `include = ["a.toml", "b.toml"]`)
	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("expected duplicate task error")
	}
	if !strings.Contains(err.Error(), "shared") {
		t.Errorf("error should name the conflicting task: %v", err)
	}
}

// TestInclude_DuplicateReminder: case 12 — duplicate reminder name.
func TestInclude_DuplicateReminder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "extra.toml", `[reminders.daily-rem]
schedule = "daily"
message  = "from extra"
`)
	root := writeRoot(t, dir, `include = ["extra.toml"]
[reminders.daily-rem]
schedule = "daily"
message  = "from root"
`)
	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("expected duplicate reminder error")
	}
	if !strings.Contains(err.Error(), "reminder") || !strings.Contains(err.Error(), "daily-rem") {
		t.Errorf("error should name the reminder: %v", err)
	}
}

// TestInclude_SameNameTaskAndReminder: case 13 — same name as task in root, reminder in include → allowed.
func TestInclude_SameNameTaskAndReminder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "extra.toml", `[reminders.shared-name]
schedule = "daily"
message  = "hello"
`)
	root := writeRoot(t, dir, `include = ["extra.toml"]
[tasks.shared-name]
command = "echo task"
`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("task and reminder may share a name: %v", err)
	}
	if _, ok := cfg.Tasks["shared-name"]; !ok {
		t.Error("expected shared-name task")
	}
	if _, ok := cfg.Reminders["shared-name"]; !ok {
		t.Error("expected shared-name reminder")
	}
}

// TestInclude_NestedInclude: case 14 — include in included file → error.
func TestInclude_NestedInclude(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "extra.toml", `include = ["other.toml"]
[tasks.extra]
command = "echo"
`)
	root := writeRoot(t, dir, `include = ["extra.toml"]`)
	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("expected nested include error")
	}
	if !strings.Contains(err.Error(), "nested includes are not supported") {
		t.Errorf("expected nested-includes error, got: %v", err)
	}
}

// TestInclude_OrbitBinInIncluded: case 15 — orbit_bin in included file → error.
func TestInclude_OrbitBinInIncluded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "extra.toml", `orbit_bin = "/usr/bin/orbit"`)
	root := writeRoot(t, dir, `include = ["extra.toml"]`)
	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("expected orbit_bin error")
	}
	if !strings.Contains(err.Error(), "orbit_bin may only be set in the root config") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestInclude_InvalidTOMLInIncluded: case 16 — invalid TOML in included file → error with path.
func TestInclude_InvalidTOMLInIncluded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bad.toml", `this is not [valid toml`)
	root := writeRoot(t, dir, `include = ["bad.toml"]`)
	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("expected TOML parse error")
	}
	if !strings.Contains(err.Error(), "bad.toml") {
		t.Errorf("error should mention included file path: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid TOML") {
		t.Errorf("error should mention 'invalid TOML': %v", err)
	}
}

// TestInclude_EmptyString: case 17 — empty string in include array → error with index.
func TestInclude_EmptyString(t *testing.T) {
	dir := t.TempDir()
	root := writeRoot(t, dir, `include = [""]`)
	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("expected error for empty include string")
	}
	if !strings.Contains(err.Error(), "empty pattern at index") {
		t.Errorf("expected empty pattern error, got: %v", err)
	}
}

// TestInclude_TildeExpansion: case 18 — ~/... resolves against home dir.
func TestInclude_TildeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Write the included file into the fake home dir.
	if err := os.MkdirAll(home+"/orbit", 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, home+"/orbit", "extra.toml", `[tasks.home-task]
command = "echo home"
`)

	dir := t.TempDir()
	root := writeRoot(t, dir, `include = ["~/orbit/extra.toml"]`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := cfg.Tasks["home-task"]; !ok {
		t.Error("expected home-task from tilde-expanded include")
	}
}

// TestInclude_TildeUserForm: case 19 — ~user/... form → error.
func TestInclude_TildeUserForm(t *testing.T) {
	dir := t.TempDir()
	root := writeRoot(t, dir, `include = ["~someuser/stuff.toml"]`)
	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("expected error for ~user form")
	}
	if !strings.Contains(err.Error(), "'~user' expansion is not supported") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestInclude_RelativeFromRootDir: case 20 — relative include resolves against root config dir even when LoadConfig called from different CWD.
func TestInclude_RelativeFromRootDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "extra.toml", `[tasks.extra]
command = "echo extra"
`)
	root := writeRoot(t, dir, `include = ["extra.toml"]`)

	// Change CWD to a different directory.
	other := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(other); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	cfg, err := LoadConfig(root) // absolute path, but CWD is different
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := cfg.Tasks["extra"]; !ok {
		t.Error("expected extra task when loading from different CWD")
	}
}

// TestInclude_DefaultsAppliedToIncludedEntries: case 21 — defaults applied to included entries.
func TestInclude_DefaultsAppliedToIncludedEntries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "extra.toml", `
[tasks.inc-task]
command  = "echo"
schedule = "daily"

[reminders.inc-rem]
schedule = "daily"
message  = "hello"
`)
	root := writeRoot(t, dir, `include = ["extra.toml"]`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	task := cfg.Tasks["inc-task"]
	if task.Retry.GetAttempts() != 3 {
		t.Errorf("included task should have default retry.attempts=3, got %d", task.Retry.GetAttempts())
	}
	rem := cfg.Reminders["inc-rem"]
	if rem.Snooze != "2h" {
		t.Errorf("included reminder should have default snooze=2h, got %q", rem.Snooze)
	}
}

// TestInclude_ValidateSuffixForIncluded: case 22 — Validate() error for broken included entry carries suffix; root entry does not.
func TestInclude_ValidateSuffixForIncluded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "extra.toml", `[tasks.broken]
# no command
schedule = "daily"
`)
	root := writeRoot(t, dir, `include = ["extra.toml"]`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing command in included file")
	}
	if !strings.Contains(err.Error(), "defined in") {
		t.Errorf("error for included entry should have provenance suffix: %v", err)
	}

	// Root file entry without command should NOT have the suffix.
	dir2 := t.TempDir()
	root2 := writeRoot(t, dir2, `[tasks.broken]
# no command
schedule = "daily"
`)
	cfg2, err := LoadConfig(root2)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	err = cfg2.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing command in root file")
	}
	if strings.Contains(err.Error(), "defined in") {
		t.Errorf("error for root entry should NOT have provenance suffix: %v", err)
	}
}

// TestInclude_OptionalExists: case 23 — "?work.toml" when file exists → merged normally.
func TestInclude_OptionalExists(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "work.toml", `[tasks.work-task]
command = "echo work"
`)
	root := writeRoot(t, dir, `include = ["?work.toml"]`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := cfg.Tasks["work-task"]; !ok {
		t.Error("expected work-task from optional include that exists")
	}
	if len(cfg.IncludeResolutions) != 1 {
		t.Fatalf("expected 1 resolution, got %d", len(cfg.IncludeResolutions))
	}
	res := cfg.IncludeResolutions[0]
	if !res.Optional {
		t.Error("expected Optional=true")
	}
	if len(res.Files) != 1 {
		t.Errorf("expected 1 file in resolution, got %d", len(res.Files))
	}
}

// TestInclude_OptionalAbsent: case 24 — "?work.toml" when absent → no error, nothing merged.
func TestInclude_OptionalAbsent(t *testing.T) {
	dir := t.TempDir()
	root := writeRoot(t, dir, `include = ["?work.toml"]`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error for absent optional include: %v", err)
	}
	if len(cfg.Tasks) != 0 {
		t.Fatalf("expected no tasks, got %d", len(cfg.Tasks))
	}
	if len(cfg.IncludeResolutions) != 1 {
		t.Fatalf("expected 1 resolution, got %d", len(cfg.IncludeResolutions))
	}
	res := cfg.IncludeResolutions[0]
	if !res.Optional {
		t.Error("expected Optional=true")
	}
	if len(res.Files) != 0 {
		t.Errorf("expected empty Files, got %v", res.Files)
	}
}

// TestInclude_QuestionMarkAlone: case 25 — "?" alone → empty pattern error.
func TestInclude_QuestionMarkAlone(t *testing.T) {
	dir := t.TempDir()
	root := writeRoot(t, dir, `include = ["?"]`)
	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("expected error for '?' alone")
	}
	if !strings.Contains(err.Error(), "empty pattern at index") {
		t.Errorf("expected empty pattern error, got: %v", err)
	}
}

// TestInclude_OptionalExistsButIsDir: case 26 — "?work.toml" exists as dir → hard error.
func TestInclude_OptionalExistsButIsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(dir+"/work.toml", 0o755); err != nil {
		t.Fatal(err)
	}
	root := writeRoot(t, dir, `include = ["?work.toml"]`)
	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("expected error when optional include is a directory")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("expected directory error, got: %v", err)
	}
}

// TestInclude_LiteralWithSpecialChars: case 27 — patterns with '?' or '[' but no '*' are literals.
func TestInclude_LiteralWithSpecialChars(t *testing.T) {
	dir := t.TempDir()
	// Create files with literal special characters in their names.
	weirdPath := dir + "/weird[1].toml"
	qPath := dir + "/?file.toml"
	if err := os.WriteFile(weirdPath, []byte("[tasks.weird]\ncommand = \"echo weird\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(qPath, []byte("[tasks.qfile]\ncommand = \"echo qfile\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Use "./" prefix so the '?' is not the optional marker.
	root := writeRoot(t, dir, `include = ["weird[1].toml", "./?file.toml"]`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error for literal paths with special chars: %v", err)
	}
	if _, ok := cfg.Tasks["weird"]; !ok {
		t.Error("expected task from weird[1].toml")
	}
	if _, ok := cfg.Tasks["qfile"]; !ok {
		t.Error("expected task from ?file.toml")
	}
}

// TestInclude_GlobEscapingBracket: case 28 — "*[1].toml" glob matches "a[1].toml" literally.
func TestInclude_GlobEscapingBracket(t *testing.T) {
	dir := t.TempDir()
	// Create a file literally named "a[1].toml".
	bracketPath := dir + "/a[1].toml"
	if err := os.WriteFile(bracketPath, []byte("[tasks.bracket]\ncommand = \"echo bracket\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := writeRoot(t, dir, `include = ["*[1].toml"]`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := cfg.Tasks["bracket"]; !ok {
		t.Errorf("expected task from a[1].toml to be loaded; tasks: %v", cfg.Tasks)
	}
}

// TestInclude_OptionalGlob: case 29 — "?orbit.d/*.toml" optional glob behaves like plain glob, Optional=true.
func TestInclude_OptionalGlob(t *testing.T) {
	dir := t.TempDir()
	root := writeRoot(t, dir, `include = ["?orbit.d/*.toml"]`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.IncludeResolutions) != 1 {
		t.Fatalf("expected 1 resolution, got %d", len(cfg.IncludeResolutions))
	}
	res := cfg.IncludeResolutions[0]
	if !res.Optional {
		t.Error("expected Optional=true")
	}
	if !res.IsGlob {
		t.Error("expected IsGlob=true")
	}
	if len(res.Files) != 0 {
		t.Errorf("expected 0 files (nothing matches), got %v", res.Files)
	}
}

func TestLoadConfig_IfFailedDefaults(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.hooked]
command           = "exit 1"
if_failed.command = "notify-send failed"

[tasks.plain]
command = "exit 0"
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Expected valid config, got %v", err)
	}

	hooked := cfg.Tasks["hooked"].IfFailed
	assertEqual(t, "IfFailed.Command", hooked.Command, "notify-send failed")
	assertEqualInt(t, "IfFailed.After", hooked.GetAfter(), 1)

	// No hook means no threshold either, so the applied config stays zero.
	plain := cfg.Tasks["plain"].IfFailed
	if plain.After != nil {
		t.Fatalf("Expected nil after without a hook command, got %d", *plain.After)
	}
}

func TestLoadConfig_IfFailedExplicitAfter(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.test]
command           = "exit 1"
if_failed.command = "notify-send failed"
if_failed.after   = 3
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Expected valid config, got %v", err)
	}
	assertEqualInt(t, "IfFailed.After", cfg.Tasks["test"].IfFailed.GetAfter(), 3)
}

func TestValidate_IfFailedAfterWithoutCommand(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.test]
command         = "exit 1"
if_failed.after = 2
`)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "if_failed.after requires if_failed.command") {
		t.Fatalf("Expected error about missing if_failed.command, got %v", err)
	}
}

func TestValidate_IfFailedAfterBelowOne(t *testing.T) {
	for _, after := range []int{0, -1} {
		cfg, err := loadFromString(fmt.Sprintf(`
[tasks.test]
command           = "exit 1"
if_failed.command = "notify"
if_failed.after   = %d
`, after))
		if err != nil {
			t.Fatalf("Load error: %v", err)
		}
		err = cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "if_failed.after must be at least 1") {
			t.Fatalf("after=%d: expected threshold error, got %v", after, err)
		}
	}
}
