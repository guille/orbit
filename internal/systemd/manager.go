package systemd

import (
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Systemctl abstracts systemctl command execution for testability.
type Systemctl interface {
	// Run executes a systemctl command and returns combined output.
	// Returns ("", nil) on success if there's no meaningful output.
	Run(args ...string) (string, error)
}

// realSystemctl executes systemctl via exec.Command.
type realSystemctl struct{}

func (realSystemctl) Run(args ...string) (string, error) {
	cmd := exec.Command("systemctl", args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// Manager handles systemd unit operations (always user-mode).
type Manager struct {
	ctl Systemctl // systemctl executor
}

// NewManager creates a new systemd manager for user-level units.
func NewManager() *Manager {
	return &Manager{ctl: realSystemctl{}}
}

// FailedServices returns, for each given task whose service unit's last run did
// not succeed, the systemd failure Result ("exit-code", "signal", "core-dump", ...).
// Successful, never-run, and unknown units are omitted. All units are queried in a
// single `systemctl show` invocation.
func (m *Manager) FailedServices(taskNames []string) (map[string]string, error) {
	if len(taskNames) == 0 {
		return nil, nil
	}

	unitToTask := make(map[string]string, len(taskNames))
	args := make([]string, 0, len(taskNames)+1)
	args = append(args, "--property=Id,Result")
	for _, name := range taskNames {
		unit := TaskServiceName(name)
		unitToTask[unit] = name
		args = append(args, unit)
	}

	output, err := m.systemctlOutput("show", args...)
	if err != nil {
		return nil, err
	}

	// systemctl show prints one property block per unit, separated by a blank
	// line. Parse each block by key so property order doesn't matter.
	failed := make(map[string]string)
	for block := range strings.SplitSeq(strings.TrimSpace(output), "\n\n") {
		var id, result string
		for line := range strings.SplitSeq(block, "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch strings.TrimSpace(key) {
			case "Id":
				id = strings.TrimSpace(value)
			case "Result":
				result = strings.TrimSpace(value)
			}
		}
		if result != "" && result != "success" {
			if task, ok := unitToTask[id]; ok {
				failed[task] = result
			}
		}
	}

	return failed, nil
}

// UnitStatus reports a unit's runtime and install state.
type UnitStatus struct {
	Active  bool // ActiveState == "active"
	Enabled bool // UnitFileState == "enabled"
}

// UnitStatuses returns the active/enabled status of each given unit, keyed by
// unit name, in a single `systemctl show` invocation. Units unknown to systemd
// report as neither active nor enabled.
func (m *Manager) UnitStatuses(units []string) (map[string]UnitStatus, error) {
	if len(units) == 0 {
		return nil, nil
	}

	args := append([]string{"--property=Id,ActiveState,UnitFileState"}, units...)
	output, err := m.systemctlOutput("show", args...)
	if err != nil {
		return nil, err
	}

	statuses := make(map[string]UnitStatus, len(units))
	for block := range strings.SplitSeq(strings.TrimSpace(output), "\n\n") {
		var id, active, enabled string
		for line := range strings.SplitSeq(block, "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch strings.TrimSpace(key) {
			case "Id":
				id = strings.TrimSpace(value)
			case "ActiveState":
				active = strings.TrimSpace(value)
			case "UnitFileState":
				enabled = strings.TrimSpace(value)
			}
		}
		if id != "" {
			statuses[id] = UnitStatus{Active: active == "active", Enabled: enabled == "enabled"}
		}
	}

	return statuses, nil
}

// RunTaskNow starts a task's service unit synchronously via `systemctl --user
// start --wait`.
// Returns an error if the unit fails or cannot be started.
func (m *Manager) RunTaskNow(taskName string) error {
	output, err := m.systemctlOutput("start", "--wait", TaskServiceName(taskName))
	if err != nil {
		return fmt.Errorf("running task %s: %w (output: %s)", taskName, err, strings.TrimSpace(output))
	}
	return nil
}

// ListUnits returns the names of all orbit-managed units known to systemd.
func (m *Manager) ListUnits() ([]string, error) {
	args := m.systemctlArgs("list-unit-files", "--all", "--no-legend", "--no-pager", orbitUnitGlob)

	output, err := m.ctl.Run(args...)
	if err != nil {
		return nil, fmt.Errorf("listing systemd units: %w", err)
	}

	var units []string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if IsOrbitUnit(name) {
			units = append(units, name)
		}
	}

	return units, nil
}

// StopAndDisableTimers stops and disables multiple timer units in batch.
func (m *Manager) StopAndDisableTimers(timerNames []string) {
	if len(timerNames) == 0 {
		return
	}
	args := append([]string{"--now"}, timerNames...)
	m.systemctl("disable", args...)
}

// EnableAndStartTimers enables and starts multiple timer units in batch.
func (m *Manager) EnableAndStartTimers(timerNames []string) {
	if len(timerNames) == 0 {
		return
	}
	args := append([]string{"--now"}, timerNames...)
	m.systemctl("enable", args...)
}

// daemonReload runs systemctl daemon-reload.
func (m *Manager) daemonReload() error {
	output, err := m.systemctlOutput("daemon-reload")
	if err != nil {
		return fmt.Errorf("reloading systemd daemon: %w (output: %s)", err, output)
	}
	return nil
}

// systemctl runs a systemctl command, ignoring output and errors.
func (m *Manager) systemctl(subcmd string, args ...string) {
	allArgs := m.systemctlArgs(subcmd, args...)
	//nolint:errcheck
	m.ctl.Run(allArgs...)
}

// systemctlOutput runs a systemctl command and returns its combined output.
func (m *Manager) systemctlOutput(subcmd string, args ...string) (string, error) {
	allArgs := m.systemctlArgs(subcmd, args...)
	return m.ctl.Run(allArgs...)
}

// systemctlArgs prepends --user.
func (m *Manager) systemctlArgs(subcmd string, args ...string) []string {
	result := make([]string, 0, 2+len(args))
	result = append(result, "--user")
	result = append(result, subcmd)
	result = append(result, args...)
	return result
}

// VerifyUnits runs systemd-analyze verify on the given unit file paths in a
// single invocation. Returns the verification output and an error if any unit
// fails.
func (m *Manager) VerifyUnits(paths ...string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	args := append([]string{"verify"}, paths...)
	cmd := exec.Command("systemd-analyze", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// NextElapses returns the next trigger time of each given OnCalendar
// expression, keyed by expression, resolved in a single `systemd-analyze
// calendar` invocation. Expressions systemd cannot resolve are omitted.
func (m *Manager) NextElapses(schedules []string) map[string]time.Time {
	if len(schedules) == 0 {
		return nil
	}

	args := append([]string{"calendar", "--iterations=1"}, schedules...)
	// A rejected expression is reported on stderr and simply yields no block on
	// stdout, so a non-zero exit says nothing about the ones that did resolve.
	output, _ := exec.Command("systemd-analyze", args...).Output()

	// systemd-analyze prints one property block per expression, separated by a
	// blank line. "Original form" echoes the expression as given, and is
	// omitted when the expression is already in normalized form.
	elapses := make(map[string]time.Time, len(schedules))
	for block := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n\n") {
		var schedule string
		var elapse time.Time
		for line := range strings.SplitSeq(block, "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			switch strings.TrimSpace(key) {
			case "Original form":
				schedule = value
			case "Normalized form":
				if schedule == "" {
					schedule = value
				}
			case "Next elapse":
				elapse, _ = parseCalendarTime(value)
			}
		}
		if schedule != "" && !elapse.IsZero() {
			elapses[schedule] = elapse
		}
	}

	return elapses
}

// parseCalendarTime parses a systemd-analyze calendar timestamp
// ("Day YYYY-MM-DD HH:MM:SS TZ").
func parseCalendarTime(s string) (time.Time, error) {
	for _, layout := range []string{
		"Mon 2006-01-02 15:04:05 MST",
		"Mon 2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05 MST",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %s", s)
}

// LogOptions controls which journal entries StreamUnitLogs shows.
type LogOptions struct {
	Follow bool
	Since  string // journalctl --since; takes precedence over Lines
	Lines  int
}

// StreamUnitLogs streams a unit's journal to the given writers via journalctl.
// It blocks until journalctl exits (indefinitely when opts.Follow is set).
func (m *Manager) StreamUnitLogs(unitName string, opts LogOptions, stdout, stderr io.Writer) error {
	c := exec.Command("journalctl", journalArgs(unitName, opts)...)
	c.Stdout = stdout
	c.Stderr = stderr
	return c.Run()
}

// journalArgs builds the journalctl arguments for a unit's logs.
func journalArgs(unitName string, opts LogOptions) []string {
	args := []string{"--user", "-u", unitName, "--no-pager"}
	if opts.Since != "" {
		args = append(args, "--since", opts.Since)
	} else {
		args = append(args, "-n", strconv.Itoa(opts.Lines))
	}
	if opts.Follow {
		args = append(args, "-f")
	}
	return args
}
