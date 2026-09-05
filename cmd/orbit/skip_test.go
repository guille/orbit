package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"go.guillerg.dev/orbit/internal/state"
	"go.guillerg.dev/orbit/internal/systemd"
)

// fiveDays fires exactly five times, all in the future, so window arithmetic
// is deterministic regardless of when the tests run.
const fiveDays = "2030-01-01..05 09:00"

func requireSystemdAnalyze(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze not available")
	}
}

func jan2030(day int) time.Time {
	return time.Date(2030, time.January, day, 9, 0, 0, 0, time.Local)
}

func TestPlanSkipNext(t *testing.T) {
	requireSystemdAnalyze(t)
	m := systemd.NewManager()
	entry := skipEntry{kind: kindTask, name: "t", schedule: fiveDays}

	tests := []struct {
		n          int
		until      time.Time
		resume     time.Time
		wantErrSub string
	}{
		{n: 1, until: jan2030(1), resume: jan2030(2)},
		{n: 4, until: jan2030(4), resume: jan2030(5)},
		// Suppressing every remaining firing is disable in disguise.
		{n: 5, wantErrSub: "skip at most 4"},
		{n: 9, wantErrSub: "skip at most 4"},
	}
	for _, tc := range tests {
		plan, err := planSkipNext(m, entry, tc.n)
		if tc.wantErrSub != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("n=%d: err = %v, want containing %q", tc.n, err, tc.wantErrSub)
			}
			continue
		}
		if err != nil {
			t.Errorf("n=%d: %v", tc.n, err)
			continue
		}
		if !plan.until.Equal(tc.until) || !plan.resume.Equal(tc.resume) || plan.count != tc.n {
			t.Errorf("n=%d: plan = %+v, want until %v resume %v count %d", tc.n, plan, tc.until, tc.resume, tc.n)
		}
	}

	oneShot := skipEntry{kind: kindReminder, name: "r", schedule: "2030-01-01 09:00"}
	if _, err := planSkipNext(m, oneShot, 1); err == nil || !strings.Contains(err.Error(), "only once more") {
		t.Errorf("one-shot: err = %v", err)
	}
	ended := skipEntry{kind: kindReminder, name: "r", schedule: "2020-01-01 09:00"}
	if _, err := planSkipNext(m, ended, 1); err == nil || !strings.Contains(err.Error(), "nothing scheduled") {
		t.Errorf("ended: err = %v", err)
	}
}

func TestPlanSkipUntil(t *testing.T) {
	requireSystemdAnalyze(t)
	m := systemd.NewManager()
	entry := skipEntry{kind: kindTask, name: "t", schedule: fiveDays}

	tests := []struct {
		name       string
		when       time.Time
		count      int
		resume     time.Time
		wantErrSub string
	}{
		{"midnight boundary", time.Date(2030, 1, 3, 0, 0, 0, 0, time.Local), 2, jan2030(3), ""},
		// A firing exactly at WHEN still happens.
		{"exact occurrence is kept", jan2030(3), 2, jan2030(3), ""},
		{"just past an occurrence", jan2030(3).Add(time.Second), 3, jan2030(4), ""},
		// Sub-second input, as a duration produces: still strictly "before".
		{"sub-second past an occurrence", jan2030(3).Add(400 * time.Millisecond), 3, jan2030(4), ""},
		{"sub-second before an occurrence", jan2030(3).Add(-400 * time.Millisecond), 2, jan2030(3), ""},
		{"before the first firing", time.Date(2029, 12, 1, 0, 0, 0, 0, time.Local), 0, time.Time{}, "no firings before"},
		{"past the last firing", time.Date(2030, 2, 1, 0, 0, 0, 0, time.Local), 0, time.Time{}, "nothing scheduled after"},
	}
	for _, tc := range tests {
		plan, err := planSkipUntil(m, entry, tc.when)
		if tc.wantErrSub != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.wantErrSub)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !plan.until.Equal(tc.when.Add(-time.Microsecond)) || plan.count != tc.count || !plan.resume.Equal(tc.resume) {
			t.Errorf("%s: plan = %+v, want count %d resume %v", tc.name, plan, tc.count, tc.resume)
		}
	}

	t.Run("count saturates", func(t *testing.T) {
		daily := skipEntry{kind: kindTask, name: "t", schedule: "daily"}
		plan, err := planSkipUntil(m, daily, time.Now().Add(200*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if plan.count != skipCountCap+1 || plan.resume.IsZero() {
			t.Errorf("plan = %+v, want count %d and a resume time", plan, skipCountCap+1)
		}
	})
}

func TestSkipResume(t *testing.T) {
	requireSystemdAnalyze(t)
	threeDaysAgo := time.Now().Add(-72 * time.Hour)
	first := jan2030(1)
	last := jan2030(5)
	var zero time.Time

	tests := []struct {
		name      string
		schedule  string
		skipUntil *time.Time
		active    bool
		resume    time.Time
	}{
		{"no skip", fiveDays, nil, false, time.Time{}},
		{"zero instant from foreign state reads as absent", fiveDays, &zero, false, time.Time{}},
		{"manual entry", "", &first, false, time.Time{}},
		{"active", fiveDays, &first, true, jan2030(2)},
		// A catch-up start long after the skipped occurrence is still inside
		// the window as long as the next occurrence has not come.
		{"expired: resume point has passed", "daily", &threeDaysAgo, false, time.Time{}},
		{"schedule ended within window", fiveDays, &last, false, time.Time{}},
	}
	for _, tc := range tests {
		resume, active := skipResume(tc.schedule, tc.skipUntil)
		if active != tc.active || !resume.Equal(tc.resume) {
			t.Errorf("%s: skipResume = %v, %v; want %v, %v", tc.name, resume, active, tc.resume, tc.active)
		}
	}
}

func TestSkippedDisplay(t *testing.T) {
	requireSystemdAnalyze(t)
	until := jan2030(1)
	taskConfig := state.AppliedTaskConfig{Schedule: fiveDays, Retry: state.AppliedRetryConfig{Attempts: 3}}
	reminderConfig := state.AppliedReminderConfig{Schedule: fiveDays}

	// Skip wins over a recorded failure; disabled wins over skip.
	failedAndSkipped := state.TaskState{FailedCycles: 2, ConsecutiveFailures: 6, RetryAttempt: 3, SkipUntil: &until}
	if got := taskStatusString(taskConfig, failedAndSkipped, true); got != "skipped" {
		t.Errorf("failed+skipped task = %q, want skipped", got)
	}
	if got := taskStatusString(taskConfig, state.TaskState{Disabled: true, SkipUntil: &until}, false); got != "disabled" {
		t.Errorf("disabled+skipped task = %q, want disabled", got)
	}
	if got := taskNextRun(taskConfig, state.TaskState{SkipUntil: &until}); !strings.HasSuffix(got, cellSkipped) {
		t.Errorf("taskNextRun = %q, want %q suffix", got, cellSkipped)
	}

	if got := reminderStatusString(reminderConfig, state.ReminderState{State: state.StateAcknowledged, SkipUntil: &until}); got != "skipped" {
		t.Errorf("skipped reminder = %q, want skipped", got)
	}
	if got := reminderNextRun(reminderConfig, state.ReminderState{SkipUntil: &until}); !strings.HasSuffix(got, cellSkipped) {
		t.Errorf("reminderNextRun = %q, want %q suffix", got, cellSkipped)
	}

	// An expired skip reads as absent.
	old := time.Now().Add(-72 * time.Hour)
	if got := reminderStatusString(state.AppliedReminderConfig{Schedule: "daily"}, state.ReminderState{State: state.StateAcknowledged, SkipUntil: &old}); got != "acknowledged" {
		t.Errorf("expired skip = %q, want acknowledged", got)
	}
}

func TestSkipResumeFailsClosed(t *testing.T) {
	// With systemd-analyze unreachable the window cannot be judged; the skip
	// must hold rather than let the run through and erase itself.
	t.Setenv("PATH", t.TempDir())
	until := time.Now().Add(-time.Hour)
	resume, active := skipResume("*-*-* 03:17:19", &until)
	if !active || !resume.IsZero() {
		t.Fatalf("skipResume = %v, %v; want active with unknown resume", resume, active)
	}
}

func TestSkipEntryUnskippable(t *testing.T) {
	tests := []struct {
		name    string
		e       skipEntry
		wantSub string
	}{
		{"ok", skipEntry{name: "x", schedule: "daily"}, ""},
		{"disabled", skipEntry{name: "x", schedule: "daily", disabled: true}, "disabled"},
		{"manual", skipEntry{name: "x"}, "no schedule"},
		{"pending", skipEntry{name: "x", schedule: "daily", pending: true}, "ack or snooze"},
	}
	for _, tc := range tests {
		err := tc.e.unskippable()
		if tc.wantSub == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.wantSub)
		}
	}
}
