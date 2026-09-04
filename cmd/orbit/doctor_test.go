package main

import (
	"strings"
	"testing"
	"time"

	"go.guillerg.dev/orbit/internal/state"
	"go.guillerg.dev/orbit/internal/systemd"
)

func TestDiagnoseTask(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	failing := state.TaskState{LastRun: past, LastExitCode: 1, ConsecutiveFailures: 6, FailedCycles: 2, RetryAttempt: 3}
	retrying := state.TaskState{LastRun: past, LastExitCode: 1, ConsecutiveFailures: 1, RetryAttempt: 1}
	clean := state.TaskState{LastRun: past}

	tests := []struct {
		name  string
		ts    state.TaskState
		st    systemd.UnitStatus
		found bool
		fatal bool
		msg   string // substring
	}{
		// systemd could not exec orbit at all: fatal regardless of state.
		{"exec failure", failing, systemd.UnitStatus{Result: "exit-code", ExitStatus: 203}, true, true, "orbit_bin"},
		{"chdir failure", clean, systemd.UnitStatus{Result: "exit-code", ExitStatus: 200}, true, true, "exit 200"},
		// Any exit other than the task-failed code is orbit's own error.
		{"orbit error", clean, systemd.UnitStatus{Result: "exit-code", ExitStatus: 1}, true, true, "orbit failed (exit 1)"},
		{"killed", clean, systemd.UnitStatus{Result: "signal"}, true, true, "killed (signal)"},
		// Exit 10 means orbit handled it: report the state record as a warning.
		{"task failed", failing, systemd.UnitStatus{Result: "exit-code", ExitStatus: 10}, true, false, "failed (2), exit 1"},
		// A cycle in flight: systemd still shows the previous success.
		{"retrying", retrying, systemd.UnitStatus{Result: "success"}, true, false, "retrying (1/3), exit 1"},
		// Nothing to report.
		{"healthy", clean, systemd.UnitStatus{Result: "success"}, false, false, ""},
		{"never ran", state.TaskState{}, systemd.UnitStatus{}, false, false, ""},
	}

	for _, tc := range tests {
		d, found := diagnoseTask(tc.ts, 3, tc.st)
		if found != tc.found {
			t.Errorf("%s: found=%v, want %v (%+v)", tc.name, found, tc.found, d)
			continue
		}
		if !found {
			continue
		}
		if d.fatal != tc.fatal {
			t.Errorf("%s: fatal=%v, want %v", tc.name, d.fatal, tc.fatal)
		}
		if !strings.Contains(d.msg, tc.msg) {
			t.Errorf("%s: msg %q does not contain %q", tc.name, d.msg, tc.msg)
		}
	}
}
