package systemd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.guillerg.dev/orbit/internal/config"
)

func TestGenerateTaskUnits(t *testing.T) {
	m := NewManager()

	units, err := m.GenerateTaskUnits("test-task", "daily", config.OnMissedRunOnce, "orbit")
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
	m := NewManager()

	units, err := m.GenerateTaskUnits("deploy", "", "", "orbit")
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
		m := NewManager()

		units, err := m.GenerateTaskUnits("test", "daily", tc.onMissed, "orbit")
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
	m := NewManager()

	units, err := m.GenerateReminderUnits("test-reminder", "hourly", "orbit")
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
	m := NewManager()

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
	m := NewManager()

	units, err := m.GenerateReminderUnits("test-reminder", "hourly", "orbit")
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

	m := NewManager()
	dir := m.UnitDir()
	if !strings.HasSuffix(dir, ".config/systemd/user") {
		t.Fatalf("Expected user systemd dir, got %s", dir)
	}

	// XDG_CONFIG_HOME override
	t.Setenv("XDG_CONFIG_HOME", "/tmp/test-xdg")

	dir2 := m.UnitDir()
	if dir2 != "/tmp/test-xdg/systemd/user" {
		t.Fatalf("Expected XDG-based dir, got %s", dir2)
	}
}

func TestClassifyChanges(t *testing.T) {
	m := NewManager()

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

	m2 := &Manager{ctl: realSystemctl{}}
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

func TestExecBin(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", `"orbit"`},
		{"orbit", `"orbit"`},
		{"/home/user/.local/share/mise/shims/orbit", `"/home/user/.local/share/mise/shims/orbit"`},
	}
	for _, tc := range tests {
		got := execBin(tc.input)
		if got != tc.want {
			t.Errorf("execBin(%q) = %s, want %s", tc.input, got, tc.want)
		}
	}
}

func TestVerifyUnits(t *testing.T) {
	m := NewManager()

	t.Run("valid unit", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "valid.service")
		content := "[Unit]\nDescription=Test\n[Service]\nType=oneshot\nExecStart=/usr/bin/env true\n"
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		out, err := m.VerifyUnits(path)
		if err != nil {
			t.Fatalf("expected no error, got: %v\noutput: %s", err, out)
		}
	})

	t.Run("bad ExecStart path", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.service")
		content := "[Unit]\nDescription=Test\n[Service]\nType=oneshot\nExecStart=\"~/.local/bin/orbit\" _run test\n"
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		out, err := m.VerifyUnits(path)
		if err == nil {
			t.Fatal("expected error for bad path, got none")
		}
		if out == "" {
			t.Fatal("expected non-empty output on failure")
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		out, err := m.VerifyUnits("/nonexistent/path/unit.service")
		if err == nil {
			t.Fatal("expected error for nonexistent file, got none")
		}
		if out == "" {
			t.Fatal("expected non-empty output on failure")
		}
	})

	t.Run("multiple units", func(t *testing.T) {
		dir := t.TempDir()
		good := filepath.Join(dir, "good.service")
		bad := filepath.Join(dir, "bad.service")
		mustWrite(t, good, "[Service]\nType=oneshot\nExecStart=/usr/bin/env true\n")
		mustWrite(t, bad, "[Service]\nType=oneshot\nExecStart=\"~/.local/bin/orbit\" _run test\n")

		out, err := m.VerifyUnits(good, bad)
		if err == nil {
			t.Fatal("expected error when one unit is invalid, got none")
		}
		if out == "" {
			t.Fatal("expected non-empty output on failure")
		}
	})
}

func TestInstallUnits_WithMock(t *testing.T) {
	tmpDir := t.TempDir()
	systemdDir := filepath.Join(tmpDir, "systemd", "user")
	stagingDir := filepath.Join(systemdDir, ".orbit-staging-test")
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		t.Fatal(err)
	}

	svcContent := "[Service]\nType=oneshot\nExecStart=/usr/bin/env true\n"
	tmrContent := "[Timer]\nOnCalendar=daily\n"

	// Write files to staging
	mustWrite(t, filepath.Join(stagingDir, "orbit-task-test.service"), svcContent)
	mustWrite(t, filepath.Join(stagingDir, "orbit-task-test.timer"), tmrContent)

	t.Run("service and timer", func(t *testing.T) {
		mock := &MockSystemctl{}
		m := &Manager{ctl: mock}
		t.Setenv("XDG_CONFIG_HOME", tmpDir)

		// Re-create staging dir (cleaned by t.TempDir)
		mustMkdirAll(t, stagingDir)
		mustWrite(t, filepath.Join(stagingDir, "orbit-task-test.service"), svcContent)
		mustWrite(t, filepath.Join(stagingDir, "orbit-task-test.timer"), tmrContent)

		units := []Unit{
			{Name: "orbit-task-test.service", Content: svcContent},
			{Name: "orbit-task-test.timer", Content: tmrContent},
		}

		err := m.InstallUnits(units, stagingDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Files moved to systemd dir
		svc := filepath.Join(systemdDir, "orbit-task-test.service")
		tmr := filepath.Join(systemdDir, "orbit-task-test.timer")
		if _, err := os.Stat(svc); os.IsNotExist(err) {
			t.Fatal("service file not moved to systemd dir")
		}
		if _, err := os.Stat(tmr); os.IsNotExist(err) {
			t.Fatal("timer file not moved to systemd dir")
		}
		// Files removed from staging
		if _, err := os.Stat(filepath.Join(stagingDir, "orbit-task-test.service")); !os.IsNotExist(err) {
			t.Error("service file still in staging dir")
		}

		// daemon-reload + enable --now
		if len(mock.Calls) != 2 {
			t.Fatalf("expected 2 systemctl calls, got %d: %v", len(mock.Calls), mock.Calls)
		}
		if mock.Calls[0][1] != "daemon-reload" {
			t.Errorf("expected daemon-reload, got %v", mock.Calls[0])
		}
		if mock.Calls[1][1] != "enable" {
			t.Errorf("expected enable, got %v", mock.Calls[1])
		}
		if !strings.HasSuffix(mock.Calls[1][3], "orbit-task-test.timer") {
			t.Errorf("expected timer in enable args, got %v", mock.Calls[1])
		}
	})

	t.Run("service only (no timer)", func(t *testing.T) {
		mock := &MockSystemctl{}
		m := &Manager{ctl: mock}
		t.Setenv("XDG_CONFIG_HOME", tmpDir)

		mustMkdirAll(t, stagingDir)
		mustWrite(t, filepath.Join(stagingDir, "orbit-task-manual.service"), svcContent)

		units := []Unit{
			{Name: "orbit-task-manual.service", Content: svcContent},
		}

		err := m.InstallUnits(units, stagingDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Only daemon-reload, no enable
		if len(mock.Calls) != 1 {
			t.Fatalf("expected 1 systemctl call (daemon-reload only), got %d: %v", len(mock.Calls), mock.Calls)
		}
		if mock.Calls[0][1] != "daemon-reload" {
			t.Errorf("expected daemon-reload, got %v", mock.Calls[0])
		}
	})
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

// mustMkdirAll creates a directory tree, failing the test on error.
func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
}

// mustWrite writes a file, failing the test on error.
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestListUnits_WithMock(t *testing.T) {
	mock := &MockSystemctl{
		Response: "orbit-task-backup.service  enabled\norbit-task-backup.timer    enabled\norbit-reminder-review.service  enabled\nsome-other.service  enabled\n",
	}
	m := &Manager{ctl: mock}

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
	m := &Manager{ctl: mock}

	_, err := m.ListUnits()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFailedServices_WithMock(t *testing.T) {
	// systemctl show prints one property block per unit, separated by a blank line.
	// "ok" succeeded, "bad" failed, "gone" is unknown to systemd (empty Result).
	// "sig" puts Result before Id to exercise order-independent parsing.
	mock := &MockSystemctl{
		Response: "Id=orbit-task-ok.service\nResult=success\n" +
			"\n" +
			"Id=orbit-task-bad.service\nResult=exit-code\n" +
			"\n" +
			"Result=signal\nId=orbit-task-sig.service\n" +
			"\n" +
			"Id=orbit-task-gone.service\nResult=\n",
	}
	m := &Manager{ctl: mock}

	failed, err := m.FailedServices([]string{"ok", "bad", "sig", "gone"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[string]string{"bad": "exit-code", "sig": "signal"}
	if len(failed) != len(want) {
		t.Fatalf("expected %v, got %v", want, failed)
	}
	for name, reason := range want {
		if failed[name] != reason {
			t.Errorf("expected %s=%s, got %q", name, reason, failed[name])
		}
	}

	// A single batched call carrying --user, the show subcommand, the Id+Result
	// property flag, and every requested unit.
	if len(mock.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.Calls))
	}
	args := mock.Calls[0]
	if args[0] != "--user" || args[1] != "show" {
		t.Fatalf("expected `--user show ...`, got %v", args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--property=Id,Result", "orbit-task-ok.service", "orbit-task-bad.service", "orbit-task-sig.service", "orbit-task-gone.service"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected args to contain %q, got %v", want, args)
		}
	}
}

func TestFailedServices_IgnoresUnrequestedUnits(t *testing.T) {
	// A block for a unit we never asked about must not leak into the result.
	mock := &MockSystemctl{
		Response: "Id=orbit-task-other.service\nResult=exit-code\n",
	}
	m := &Manager{ctl: mock}

	failed, err := m.FailedServices([]string{"asked"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("expected no failures, got %v", failed)
	}
}

func TestFailedServices_Empty(t *testing.T) {
	mock := &MockSystemctl{}
	m := &Manager{ctl: mock}

	failed, err := m.FailedServices(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if failed != nil {
		t.Errorf("expected nil map, got %v", failed)
	}
	if len(mock.Calls) != 0 {
		t.Errorf("expected no systemctl calls for empty input, got %d", len(mock.Calls))
	}
}

func TestFailedServices_Error(t *testing.T) {
	mock := &MockSystemctl{Err: fmt.Errorf("systemctl failed")}
	m := &Manager{ctl: mock}

	if _, err := m.FailedServices([]string{"x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestRemoveUnits_WithMock(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &MockSystemctl{}

	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	m := &Manager{ctl: mock}

	// Create files to remove
	systemdDir := filepath.Join(tmpDir, "systemd", "user")
	mustMkdirAll(t, systemdDir)
	mustWrite(t, filepath.Join(systemdDir, "orbit-task-old.service"), "x")
	mustWrite(t, filepath.Join(systemdDir, "orbit-task-old.timer"), "x")

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
