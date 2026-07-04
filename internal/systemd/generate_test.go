package systemd

import (
	"strings"
	"testing"
	"time"

	"go.guillerg.dev/orbit/internal/config"
)

func TestGenerateTaskUnits(t *testing.T) {
	units, err := GenerateTaskUnits("test-task", "daily", config.OnMissedRunOnce, "orbit")
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
	units, err := GenerateTaskUnits("deploy", "", "", "orbit")
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
		units, err := GenerateTaskUnits("test", "daily", tc.onMissed, "orbit")
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
	units, err := GenerateReminderUnits("test-reminder", "hourly", "orbit")
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
	snoozeTime := time.Date(2026, 5, 7, 15, 30, 0, 0, time.UTC)
	unit, err := GenerateSnoozeTimer("weekly-review", snoozeTime)
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
	units, err := GenerateReminderUnits("test-reminder", "hourly", "orbit")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(units) != 2 {
		t.Fatalf("Expected 2 units (service + timer), got %d", len(units))
	}

	svc := units[0]
	assertContains(t, svc.Content, "_notify test-reminder")
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

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("Expected content to contain %q, got:\n%s", substr, s)
	}
}
