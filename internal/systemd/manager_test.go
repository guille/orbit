package systemd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guille/orbit/internal/config"
)

func TestGenerateTaskUnits(t *testing.T) {
	m := NewManager("/usr/bin/orbit")

	units, err := m.GenerateTaskUnits("test-task", "daily", config.OnMissedRunOnce)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(units) != 2 {
		t.Fatalf("Expected 2 units (service + timer), got %d", len(units))
	}

	// Service unit
	svc := units[0]
	if svc.Name != "orbit-task-test-task.service" {
		t.Fatalf("Expected service name orbit-task-test-task.service, got %s", svc.Name)
	}
	assertContains(t, svc.Content, "[Service]")
	assertContains(t, svc.Content, "Type=oneshot")
	assertContains(t, svc.Content, "_run test-task")

	// Timer unit
	tmr := units[1]
	if tmr.Name != "orbit-task-test-task.timer" {
		t.Fatalf("Expected timer name orbit-task-test-task.timer, got %s", tmr.Name)
	}
	assertContains(t, tmr.Content, "[Timer]")
	assertContains(t, tmr.Content, "OnCalendar=daily")
	assertContains(t, tmr.Content, "Persistent=true")
	assertContains(t, tmr.Content, "[Install]")
	assertContains(t, tmr.Content, "WantedBy=timers.target")
}

func TestGenerateTaskUnits_NoSchedule(t *testing.T) {
	m := NewManager("/usr/bin/orbit")

	units, err := m.GenerateTaskUnits("deploy", "", "")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(units) != 1 {
		t.Fatalf("Expected 1 unit (service only), got %d", len(units))
	}

	svc := units[0]
	if svc.Name != "orbit-task-deploy.service" {
		t.Fatalf("Expected service name orbit-task-deploy.service, got %s", svc.Name)
	}
	assertContains(t, svc.Content, "[Service]")
	assertContains(t, svc.Content, "Type=oneshot")
	assertContains(t, svc.Content, "_run deploy")
}

func TestGenerateTaskUnits_OnMissedPersistent(t *testing.T) {
	tests := []struct {
		onMissed           config.OnMissedPolicy
		expectedPersistent string
	}{
		{config.OnMissedRunOnce, "true"},
		{config.OnMissedSkip, "false"},
	}

	for _, tc := range tests {
		m := NewManager("/usr/bin/orbit")

		units, err := m.GenerateTaskUnits("test", "daily", tc.onMissed)
		if err != nil {
			t.Fatalf("Unexpected error for on_missed=%s: %v", tc.onMissed, err)
		}

		tmr := units[1]
		expected := "Persistent=" + tc.expectedPersistent
		assertContains(t, tmr.Content, expected)
		// Should NOT have OnUnitActiveSec at all
		if strings.Contains(tmr.Content, "OnUnitActiveSec") {
			t.Fatalf("Timer for on_missed=%s should not contain OnUnitActiveSec", tc.onMissed)
		}
	}
}

func TestGenerateReminderUnits(t *testing.T) {
	m := NewManager("/usr/bin/orbit")

	units, err := m.GenerateReminderUnits("test-reminder", "hourly")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(units) != 2 {
		t.Fatalf("Expected 2 units (service + timer), got %d", len(units))
	}

	// Service
	svc := units[0]
	if svc.Name != "orbit-reminder-test-reminder.service" {
		t.Fatalf("Expected service name orbit-reminder-test-reminder.service, got %s", svc.Name)
	}
	assertContains(t, svc.Content, "[Service]")
	assertContains(t, svc.Content, "Type=oneshot")
	assertContains(t, svc.Content, "_notify test-reminder")

	// Timer
	tmr := units[1]
	if tmr.Name != "orbit-reminder-test-reminder.timer" {
		t.Fatalf("Expected timer name orbit-reminder-test-reminder.timer, got %s", tmr.Name)
	}
	assertContains(t, tmr.Content, "[Timer]")
	assertContains(t, tmr.Content, "OnCalendar=hourly")
	assertContains(t, tmr.Content, "Persistent=true")
}

func TestGenerateSnoozeTimer(t *testing.T) {
	m := NewManager("/usr/bin/orbit")

	snoozeTime := time.Date(2026, 5, 7, 15, 30, 0, 0, time.UTC)
	unit, err := m.GenerateSnoozeTimer("weekly-review", snoozeTime)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if unit.Name != "orbit-snooze-weekly-review.timer" {
		t.Fatalf("Expected timer name orbit-snooze-weekly-review.timer, got %s", unit.Name)
	}

	assertContains(t, unit.Content, "[Timer]")
	assertContains(t, unit.Content, "OnCalendar=2026-05-07 15:30:00")
	assertContains(t, unit.Content, "Persistent=true")
	assertContains(t, unit.Content, "Unit=orbit-reminder-weekly-review.service")
}

func TestGenerateReminderUnits_NoCommand(t *testing.T) {
	m := NewManager("/usr/bin/orbit")

	units, err := m.GenerateReminderUnits("test-reminder", "hourly")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(units) != 2 {
		t.Fatalf("Expected 2 units (service + timer), got %d", len(units))
	}

	svc := units[0]
	assertContains(t, svc.Content, "_notify test-reminder")
}

func TestUnitDir(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}

	m := NewManager("/usr/bin/orbit")
	dir := m.unitDir()
	if !strings.HasSuffix(dir, ".config/systemd/user") {
		t.Fatalf("Expected user systemd dir, got %s", dir)
	}

	// XDG_CONFIG_HOME override
	t.Setenv("XDG_CONFIG_HOME", "/tmp/test-xdg")

	dir2 := m.unitDir()
	if dir2 != "/tmp/test-xdg/systemd/user" {
		t.Fatalf("Expected XDG-based dir, got %s", dir2)
	}
}

func TestClassifyChanges(t *testing.T) {
	m := NewManager("/usr/bin/orbit")

	// All new (nothing existing) -> all creates
	desired := []Unit{
		{Name: "orbit-task-a.service", Content: "content-a"},
		{Name: "orbit-task-a.timer", Content: "timer-a"},
		{Name: "orbit-task-b.service", Content: "content-b"},
	}

	cs := m.ClassifyChanges(desired, nil)
	if len(cs.Create) != 3 {
		t.Fatalf("Expected 3 creates, got %d", len(cs.Create))
	}
	if len(cs.Update) != 0 {
		t.Fatalf("Expected 0 updates, got %d", len(cs.Update))
	}
	if len(cs.Remove) != 0 {
		t.Fatalf("Expected 0 removes, got %d", len(cs.Remove))
	}

	// Orphan units (existing but not desired)
	cs2 := m.ClassifyChanges(nil, []string{"orbit-task-old.service", "orbit-task-old.timer"})
	if len(cs2.Remove) != 2 {
		t.Fatalf("Expected 2 removes, got %d", len(cs2.Remove))
	}
	if len(cs2.Create) != 0 {
		t.Fatalf("Expected 0 creates with nil desired, got %d", len(cs2.Create))
	}

	// Mixed: test classification when a unit is in existing list but file can't be read.
	t.Setenv("XDG_CONFIG_HOME", "/nonexistent-path-for-test")

	m2 := &Manager{orbitPath: "/usr/bin/orbit", ctl: realSystemctl{}}
	// unitDir points to /nonexistent-path-for-test/systemd/user — files unreadable
	existing := []string{"orbit-task-a.service"}
	cs3 := m2.ClassifyChanges(desired, existing)
	// orbit-task-a.service: in existing, but can't read file -> create
	// orbit-task-a.timer: not in existing -> create
	// orbit-task-b.service: not in existing -> create
	if len(cs3.Create) != 3 {
		t.Fatalf("Expected 3 creates (unreadable + new), got %d", len(cs3.Create))
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("Expected content to contain %q, got:\n%s", substr, s)
	}
}

// --- Mock systemctl tests ---

// MockSystemctl records calls and returns predefined responses.
type MockSystemctl struct {
	Calls    [][]string
	Response string
	Err      error
}

func (m *MockSystemctl) Run(args ...string) (string, error) {
	m.Calls = append(m.Calls, args)
	return m.Response, m.Err
}

func TestListUnits_WithMock(t *testing.T) {
	mock := &MockSystemctl{
		Response: "orbit-task-backup.service  enabled\norbit-task-backup.timer    enabled\norbit-reminder-review.service  enabled\nsome-other.service  enabled\n",
	}
	m := &Manager{orbitPath: "/usr/bin/orbit", ctl: mock}

	units, err := m.ListUnits()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(units) != 3 {
		t.Fatalf("expected 3 orbit units, got %d: %v", len(units), units)
	}

	// Verify --user was passed
	if len(mock.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.Calls))
	}
	args := mock.Calls[0]
	if args[0] != "--user" {
		t.Errorf("expected --user flag, got %v", args)
	}
}

func TestListUnits_Error(t *testing.T) {
	mock := &MockSystemctl{Err: fmt.Errorf("systemctl failed")}
	m := &Manager{orbitPath: "/usr/bin/orbit", ctl: mock}

	_, err := m.ListUnits()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestApplyUnits_WithMock(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &MockSystemctl{}

	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	m := &Manager{orbitPath: "/usr/bin/orbit", ctl: mock}

	units := []Unit{
		{Name: "orbit-task-test.service", Content: "[Service]\nType=oneshot\n"},
		{Name: "orbit-task-test.timer", Content: "[Timer]\nOnCalendar=daily\n"},
	}

	err := m.ApplyUnits(units)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have called daemon-reload, then enable --now for the timer
	if len(mock.Calls) != 2 {
		t.Fatalf("expected 2 systemctl calls, got %d: %v", len(mock.Calls), mock.Calls)
	}

	// First call: daemon-reload
	if mock.Calls[0][1] != "daemon-reload" {
		t.Errorf("expected daemon-reload, got %v", mock.Calls[0])
	}
	// Second call: enable --now timer
	if mock.Calls[1][1] != "enable" {
		t.Errorf("expected enable, got %v", mock.Calls[1])
	}

	// Verify files were written
	systemdDir := filepath.Join(tmpDir, "systemd", "user")
	content, err := os.ReadFile(filepath.Join(systemdDir, "orbit-task-test.service"))
	if err != nil {
		t.Fatalf("failed to read service file: %v", err)
	}
	if string(content) != "[Service]\nType=oneshot\n" {
		t.Errorf("unexpected service content: %s", content)
	}
}

func TestRemoveUnits_WithMock(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &MockSystemctl{}

	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	m := &Manager{orbitPath: "/usr/bin/orbit", ctl: mock}

	// Create files to remove
	systemdDir := filepath.Join(tmpDir, "systemd", "user")
	//nolint:errcheck
	os.MkdirAll(systemdDir, 0755)
	//nolint:errcheck
	os.WriteFile(filepath.Join(systemdDir, "orbit-task-old.service"), []byte("x"), 0644)
	//nolint:errcheck
	os.WriteFile(filepath.Join(systemdDir, "orbit-task-old.timer"), []byte("x"), 0644)

	units := []Unit{
		{Name: "orbit-task-old.service"},
		{Name: "orbit-task-old.timer"},
	}

	err := m.RemoveUnits(units)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have: batch stop (both units), batch disable (timer), daemon-reload = 3
	if len(mock.Calls) != 3 {
		t.Fatalf("expected 3 systemctl calls, got %d: %v", len(mock.Calls), mock.Calls)
	}

	// Verify files deleted
	if _, err := os.Stat(filepath.Join(systemdDir, "orbit-task-old.service")); !os.IsNotExist(err) {
		t.Error("expected service file to be deleted")
	}
	if _, err := os.Stat(filepath.Join(systemdDir, "orbit-task-old.timer")); !os.IsNotExist(err) {
		t.Error("expected timer file to be deleted")
	}
}
