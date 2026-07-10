//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDoctorDetectsDrift applies a config, deletes an installed unit behind
// orbit's back, and expects doctor to report the missing unit.
func TestDoctorDetectsDrift(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, "[tasks.backup]\nschedule = \"daily\"\ncommand = \"echo hi\"\n"); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	// Simulate drift: remove the timer unit from disk.
	if err := os.Remove(filepath.Join(e.unitDir(), "orbit-task-backup.timer")); err != nil {
		t.Fatal(err)
	}

	r := e.run(t, "", "doctor")
	if !contains(r.stdout, "MISSING") || !contains(r.stdout, "orbit-task-backup.timer") {
		t.Errorf("expected doctor to flag the missing timer, got:\n%s", r.stdout)
	}
	if r.exit == 0 {
		t.Errorf("expected non-zero exit when drift is present, got 0")
	}
}

// TestDoctorClean applies a scheduled task and expects every doctor check to
// pass: no drift, timer reported enabled and active, exit 0.
func TestDoctorClean(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, "[tasks.backup]\nschedule = \"daily\"\ncommand = \"echo hi\"\n"); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	r := e.run(t, "", "doctor")
	if contains(r.stdout, "MISSING") || contains(r.stdout, "DRIFTED") || contains(r.stdout, "ORPHAN") {
		t.Errorf("expected no drift after a clean apply, got:\n%s", r.stdout)
	}
	if r.exit != 0 {
		t.Errorf("doctor should exit 0 after a clean apply, got %d\nstdout: %s\nstderr: %s", r.exit, r.stdout, r.stderr)
	}
	if !contains(r.stdout, "All checks passed") {
		t.Errorf("expected 'All checks passed', got:\n%s", r.stdout)
	}
}
