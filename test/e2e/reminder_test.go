//go:build e2e

package e2e

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// TestReminderSentinelLifecycle covers the pending-sentinel flow that drives the
// shell prompt: firing a reminder creates the sentinel, acknowledging removes it.
func TestReminderSentinelLifecycle(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, "[reminders.standup]\nschedule = \"Mon *-*-* 09:00:00\"\nmessage = \"Standup\"\n"); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}

	// No pending reminders yet: no sentinel.
	if _, err := os.Stat(e.sentinelPath()); !os.IsNotExist(err) {
		t.Fatalf("expected no sentinel before firing, stat err=%v", err)
	}

	// Fire the reminder (what the systemd service does): becomes pending.
	if r := e.run(t, "", "_notify", "standup"); r.exit != 0 {
		t.Fatalf("_notify: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if _, err := os.Stat(e.sentinelPath()); err != nil {
		t.Fatalf("expected sentinel after firing: %v", err)
	}

	// Acknowledge: sentinel is removed.
	if r := e.run(t, "", "ack", "standup"); r.exit != 0 {
		t.Fatalf("ack: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if _, err := os.Stat(e.sentinelPath()); !os.IsNotExist(err) {
		t.Errorf("expected sentinel removed after ack, stat err=%v", err)
	}
}

// TestDisableDismissesPending verifies the invariant that disabling a pending
// reminder dismisses it, clearing the sentinel. Piped stdin is non-interactive,
// so disable proceeds without a prompt.
func TestDisableDismissesPending(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, "[reminders.standup]\nschedule = \"Mon *-*-* 09:00:00\"\nmessage = \"Standup\"\n"); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "_notify", "standup"); r.exit != 0 {
		t.Fatalf("_notify: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if _, err := os.Stat(e.sentinelPath()); err != nil {
		t.Fatalf("expected sentinel after firing: %v", err)
	}

	if r := e.run(t, "", "disable", "standup"); r.exit != 0 {
		t.Fatalf("disable: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if _, err := os.Stat(e.sentinelPath()); !os.IsNotExist(err) {
		t.Errorf("expected sentinel cleared after disabling a pending reminder, stat err=%v", err)
	}
}

// TestNotifyFailureStillFires verifies a failed popup fails the unit but the
// reminder is still recorded as pending, so the prompt sentinel shows it.
func TestNotifyFailureStillFires(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, "[reminders.standup]\nschedule = \"Mon *-*-* 09:00:00\"\nmessage = \"Standup\"\n"); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	e.breakNotifier(t)

	r := e.run(t, "", "_notify", "standup")
	if r.exit == 0 {
		t.Fatalf("_notify: expected non-zero exit when notify-send fails, stderr=%s", r.stderr)
	}
	if !strings.Contains(r.stderr, "Cannot connect to notification server") {
		t.Errorf("stderr should carry notify-send's output, got %q", r.stderr)
	}
	if _, err := os.Stat(e.sentinelPath()); err != nil {
		t.Fatalf("expected sentinel after a failed send: %v", err)
	}
}

// TestNotifyUrgency verifies the configured urgency reaches notify-send.
func TestNotifyUrgency(t *testing.T) {
	requireSystemdAnalyze(t)

	e := newEnv(t)
	if r := e.applyConfig(t, "[reminders.standup]\nschedule = \"Mon *-*-* 09:00:00\"\nmessage = \"Standup\"\nurgency = \"critical\"\n"); r.exit != 0 {
		t.Fatalf("apply: exit=%d stderr=%s", r.exit, r.stderr)
	}
	if r := e.run(t, "", "_notify", "standup"); r.exit != 0 {
		t.Fatalf("_notify: exit=%d stderr=%s", r.exit, r.stderr)
	}

	args := e.lastNotifyArgs(t)
	i := slices.Index(args, "--urgency")
	if i < 0 || i+1 >= len(args) || args[i+1] != "critical" {
		t.Errorf("expected --urgency critical in notify-send args, got %q", args)
	}
}
