//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIncludeApply verifies that orbit apply with a root config including one
// extra file merges entries from both, and orbit list shows all of them.
func TestIncludeApply(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	configDir := filepath.Join(e.home, ".config", "orbit")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write the extra included file.
	extraToml := `[tasks.extra-task]
command  = "echo extra"
schedule = "daily"
`
	if err := os.WriteFile(filepath.Join(configDir, "extra.toml"), []byte(extraToml), 0o644); err != nil {
		t.Fatal(err)
	}

	// Root config references extra.toml.
	rootToml := "orbit_bin = \"" + orbitBin + "\"\n" +
		"include = [\"extra.toml\"]\n\n" +
		"[tasks.root-task]\n" +
		"command  = \"echo root\"\n" +
		"schedule = \"daily\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "orbit.toml"), []byte(rootToml), 0o644); err != nil {
		t.Fatal(err)
	}

	r := e.run(t, "", "apply")
	if r.exit != 0 {
		t.Fatalf("apply exit=%d\nstdout: %s\nstderr: %s", r.exit, r.stdout, r.stderr)
	}

	// Both units should be installed on disk.
	for _, name := range []string{
		"orbit-task-root-task.service",
		"orbit-task-root-task.timer",
		"orbit-task-extra-task.service",
		"orbit-task-extra-task.timer",
	} {
		if _, err := os.Stat(filepath.Join(e.unitDir(), name)); err != nil {
			t.Errorf("expected unit %s installed: %v", name, err)
		}
	}

	// orbit list should show both tasks.
	r = e.run(t, "", "list")
	if !strings.Contains(r.stdout+r.stderr, "root-task") {
		t.Errorf("expected root-task in list output:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout+r.stderr, "extra-task") {
		t.Errorf("expected extra-task in list output:\n%s", r.stdout)
	}
}

// TestIncludeApplyMissingFile verifies that removing the included file and
// re-running orbit apply fails at load, leaving the applied state untouched.
func TestIncludeApplyMissingFile(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	configDir := filepath.Join(e.home, ".config", "orbit")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}

	extraPath := filepath.Join(configDir, "extra.toml")
	extraToml := "[tasks.extra-task]\ncommand = \"echo extra\"\nschedule = \"daily\"\n"
	if err := os.WriteFile(extraPath, []byte(extraToml), 0o644); err != nil {
		t.Fatal(err)
	}

	rootToml := "orbit_bin = \"" + orbitBin + "\"\n" +
		"include = [\"extra.toml\"]\n\n" +
		"[tasks.root-task]\n" +
		"command  = \"echo root\"\n" +
		"schedule = \"daily\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "orbit.toml"), []byte(rootToml), 0o644); err != nil {
		t.Fatal(err)
	}

	// First apply succeeds.
	if r := e.run(t, "", "apply"); r.exit != 0 {
		t.Fatalf("first apply failed: exit=%d stderr=%s", r.exit, r.stderr)
	}

	// Remove the included file.
	if err := os.Remove(extraPath); err != nil {
		t.Fatal(err)
	}

	// Second apply should fail at load.
	r := e.run(t, "", "apply")
	if r.exit == 0 {
		t.Error("expected non-zero exit when included file is missing")
	}
	// Error message should mention the missing file.
	combined := r.stdout + r.stderr
	if !strings.Contains(combined, "file not found") && !strings.Contains(combined, "extra.toml") {
		t.Errorf("expected error mentioning missing include, got:\nstdout: %s\nstderr: %s", r.stdout, r.stderr)
	}

	// Applied state should be untouched — both tasks still visible in list.
	r = e.run(t, "", "list")
	if !strings.Contains(r.stdout+r.stderr, "extra-task") {
		t.Errorf("applied state should still contain extra-task after failed apply:\n%s", r.stdout)
	}
}

// TestDoctorEmptyGlobWarning verifies that doctor prints a WARNING (not FAIL)
// and exits 0 when a non-optional glob pattern matches no files.
func TestDoctorEmptyGlobWarning(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	// Apply first so doctor can reach the includes check.
	rootToml := "orbit_bin = \"" + orbitBin + "\"\n" +
		"include = [\"orbit.d/*.toml\"]\n\n" +
		"[tasks.my-task]\n" +
		"command  = \"echo hello\"\n" +
		"schedule = \"daily\"\n"
	e.writeConfig(t, rootToml)

	// Apply succeeds (empty glob is OK).
	if r := e.run(t, "", "apply"); r.exit != 0 {
		t.Fatalf("apply failed: exit=%d stderr=%s", r.exit, r.stderr)
	}

	r := e.run(t, "", "doctor")
	// Should exit 0 — empty glob never fails doctor.
	if r.exit != 0 {
		t.Errorf("doctor should exit 0 with empty glob, got %d\nstdout: %s\nstderr: %s", r.exit, r.stdout, r.stderr)
	}
	// Should print WARNING for the empty glob.
	if !strings.Contains(r.stdout, "WARNING") {
		t.Errorf("expected WARNING in doctor output for empty glob:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "matched no files") {
		t.Errorf("expected 'matched no files' in doctor output:\n%s", r.stdout)
	}
	// Should print the "Checking includes" line.
	if !strings.Contains(r.stdout, "Checking includes") {
		t.Errorf("expected 'Checking includes' in doctor output:\n%s", r.stdout)
	}
}

// TestDoctorNoIncludesNoCheck verifies that doctor does NOT print a
// "Checking includes" line when the config has no include key.
func TestDoctorNoIncludesNoCheck(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, "[tasks.backup]\nschedule = \"daily\"\ncommand = \"echo hi\"\n"); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	r := e.run(t, "", "doctor")
	if strings.Contains(r.stdout, "Checking includes") {
		t.Errorf("doctor should NOT print 'Checking includes' when no includes: %s", r.stdout)
	}
}
