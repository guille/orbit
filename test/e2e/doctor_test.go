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

// TestDoctorOrbitCannotRun: systemd reports the task service exited 203 (exec
// failure), which orbit's own state knows nothing about. Doctor must fail and
// point at orbit_bin.
func TestDoctorOrbitCannotRun(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, "[tasks.backup]\nschedule = \"daily\"\ncommand = \"echo hi\"\n"); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	e.setFakeResult(t, "orbit-task-backup.service", "exit-code", 203)

	r := e.run(t, "", "doctor")
	if r.exit == 0 {
		t.Errorf("doctor should fail when orbit could not be executed, got exit 0\n%s", r.stdout)
	}
	if !contains(r.stdout, "FAIL: backup orbit could not be executed (exit 203)") || !contains(r.stdout, "orbit_bin") {
		t.Errorf("expected an exec-failure diagnosis naming orbit_bin, got:\n%s", r.stdout)
	}
}

// TestDoctorTaskFailureWarns: the command itself failed, orbit recorded it and
// exited 10. That is the task's problem, not orbit's: doctor warns but passes.
func TestDoctorTaskFailureWarns(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	cfg := "[tasks.backup]\nschedule = \"daily\"\ncommand = \"exit 2\"\nretry.attempts = 1\nretry.delay = \"0s\"\n"
	if r := e.applyConfig(t, cfg); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "_run", "backup"); r.exit != 10 {
		t.Fatalf("_run: exit=%d stderr=%s", r.exit, r.stderr)
	}
	e.setFakeResult(t, "orbit-task-backup.service", "exit-code", 10)

	r := e.run(t, "", "doctor")
	if r.exit != 0 {
		t.Errorf("a failing command must not fail doctor, got exit %d\n%s", r.exit, r.stdout)
	}
	if !contains(r.stdout, "WARNING: backup failed (1), exit 2") {
		t.Errorf("expected a task-failure warning, got:\n%s", r.stdout)
	}
	if contains(r.stdout, "FAIL") {
		t.Errorf("unexpected FAIL in doctor output:\n%s", r.stdout)
	}
}

// TestDoctorBatchesUnitQueries: the task, timer, and disabled-unit checks share
// a single `systemctl show` round trip.
func TestDoctorBatchesUnitQueries(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, threeEntryConfig); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "disable", "deploy"); r.exit != 0 {
		t.Fatalf("disable: exit=%d stderr=%s", r.exit, r.stderr)
	}

	// The fake replays this log to answer status queries, so count rather
	// than reset.
	before := countShowCalls(e.systemctlCalls(t))
	if r := e.run(t, "", "doctor"); r.exit != 0 {
		t.Fatalf("doctor: exit=%d\n%s", r.exit, r.stdout)
	}
	calls := e.systemctlCalls(t)
	if shows := countShowCalls(calls) - before; shows != 1 {
		t.Errorf("expected exactly 1 systemctl show call, got %d:\n%s", shows, joinLines(calls[len(calls)-shows-3:]))
	}
}

func countShowCalls(calls []string) int {
	n := 0
	for _, c := range calls {
		if contains(c, " show ") {
			n++
		}
	}
	return n
}
