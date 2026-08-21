package systemd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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

func TestJournalArgs(t *testing.T) {
	unit := "orbit-task-backup.service"

	tests := []struct {
		name string
		opts LogOptions
		want []string
	}{
		{
			"lines by default",
			LogOptions{Lines: 50},
			[]string{"--user", "-u", unit, "--no-pager", "-n", "50"},
		},
		{
			"since takes precedence over lines",
			LogOptions{Since: "1 hour ago", Lines: 50},
			[]string{"--user", "-u", unit, "--no-pager", "--since", "1 hour ago"},
		},
		{
			"follow appends -f",
			LogOptions{Lines: 20, Follow: true},
			[]string{"--user", "-u", unit, "--no-pager", "-n", "20", "-f"},
		},
	}

	for _, tc := range tests {
		got := journalArgs(unit, tc.opts)
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s: journalArgs = %v, want %v", tc.name, got, tc.want)
		}
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
	// The glob keeps systemd from enumerating every unit on the system.
	if !slices.Contains(args, orbitUnitGlob) {
		t.Errorf("expected %q pattern, got %v", orbitUnitGlob, args)
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

func TestUnitStatuses_WithMock(t *testing.T) {
	// "good" is active+enabled; "stale" is active but not enabled (UnitFileState
	// before ActiveState to exercise order-independent parsing); "gone" is unknown
	// to systemd (inactive, empty UnitFileState).
	mock := &MockSystemctl{
		Response: "Id=orbit-task-good.timer\nActiveState=active\nUnitFileState=enabled\n" +
			"\n" +
			"Id=orbit-task-stale.timer\nUnitFileState=disabled\nActiveState=active\n" +
			"\n" +
			"Id=orbit-task-gone.timer\nActiveState=inactive\nUnitFileState=\n",
	}
	m := &Manager{ctl: mock}

	units := []string{"orbit-task-good.timer", "orbit-task-stale.timer", "orbit-task-gone.timer"}
	statuses, err := m.UnitStatuses(units)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[string]UnitStatus{
		"orbit-task-good.timer":  {Active: true, Enabled: true},
		"orbit-task-stale.timer": {Active: true, Enabled: false},
		"orbit-task-gone.timer":  {Active: false, Enabled: false},
	}
	for unit, w := range want {
		if statuses[unit] != w {
			t.Errorf("%s: got %+v, want %+v", unit, statuses[unit], w)
		}
	}

	if len(mock.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.Calls))
	}
	joined := strings.Join(mock.Calls[0], " ")
	if !strings.Contains(joined, "show") || !strings.Contains(joined, "--property=Id,ActiveState,UnitFileState") {
		t.Errorf("expected `show --property=Id,ActiveState,UnitFileState`, got %v", mock.Calls[0])
	}
}

func TestUnitStatuses_Empty(t *testing.T) {
	mock := &MockSystemctl{}
	m := &Manager{ctl: mock}

	statuses, err := m.UnitStatuses(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if statuses != nil {
		t.Errorf("expected nil map, got %v", statuses)
	}
	if len(mock.Calls) != 0 {
		t.Errorf("expected no systemctl calls for empty input, got %d", len(mock.Calls))
	}
}

func TestRunTaskNow_WithMock(t *testing.T) {
	mock := &MockSystemctl{}
	m := &Manager{ctl: mock}

	if err := m.RunTaskNow("backup"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.Calls))
	}
	want := []string{"--user", "start", "--wait", "orbit-task-backup.service"}
	if strings.Join(mock.Calls[0], " ") != strings.Join(want, " ") {
		t.Errorf("expected %v, got %v", want, mock.Calls[0])
	}
}

func TestRunTaskNow_Error(t *testing.T) {
	// systemctl --wait exits non-zero when the started unit fails.
	mock := &MockSystemctl{Response: "Job for orbit-task-backup.service failed", Err: fmt.Errorf("exit status 1")}
	m := &Manager{ctl: mock}

	err := m.RunTaskNow("backup")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "backup") {
		t.Errorf("expected error to mention task name, got %q", err.Error())
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
