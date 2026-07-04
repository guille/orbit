package systemd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
