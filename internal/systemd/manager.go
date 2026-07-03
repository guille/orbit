package systemd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"go.guillerg.dev/orbit/internal/config"
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

// execBin returns the quoted command token for ExecStart.
// Empty defaults to bare "orbit".
func execBin(orbitBin string) string {
	if orbitBin == "" {
		orbitBin = "orbit"
	}
	return fmt.Sprintf("%q", orbitBin)
}

// NewManager creates a new systemd manager for user-level units.
func NewManager() *Manager {
	return &Manager{ctl: realSystemctl{}}
}

// Unit represents a systemd unit file.
type Unit struct {
	Name       string
	Content    string
	OldContent string // populated for updates only: the previous on-disk content
}

// Unit name helpers — single source of truth for orbit unit naming.

// TaskServiceName returns the systemd service unit name for a task.
func TaskServiceName(name string) string {
	return "orbit-task-" + name + ".service"
}

// TaskTimerName returns the systemd timer unit name for a task.
func TaskTimerName(name string) string {
	return "orbit-task-" + name + ".timer"
}

// ReminderServiceName returns the systemd service unit name for a reminder.
func ReminderServiceName(name string) string {
	return "orbit-reminder-" + name + ".service"
}

// ReminderTimerName returns the systemd timer unit name for a reminder.
func ReminderTimerName(name string) string {
	return "orbit-reminder-" + name + ".timer"
}

// SnoozeTimerName returns the systemd timer unit name for a snoozed reminder.
func SnoozeTimerName(name string) string {
	return "orbit-snooze-" + name + ".timer"
}

// IsOrbitUnit returns true if the unit name is managed by orbit.
func IsOrbitUnit(name string) bool {
	return strings.HasPrefix(name, "orbit-task-") ||
		strings.HasPrefix(name, "orbit-reminder-") ||
		strings.HasPrefix(name, "orbit-snooze-")
}

var serviceTemplate = template.Must(template.New("service").Parse(`[Unit]
Description=Orbit task {{.Name}}

[Service]
Type=oneshot
WorkingDirectory=%h
ExecStart={{.ExecCommand}}
`))

var timerTemplate = template.Must(template.New("timer").Parse(`[Unit]
Description=Timer for orbit task {{.Name}}

[Timer]
OnCalendar={{.Schedule}}
Persistent={{.Persistent}}

[Install]
WantedBy=timers.target
`))

var reminderServiceTemplate = template.Must(template.New("reminder-service").Parse(`[Unit]
Description=Orbit reminder {{.Name}}

[Service]
Type=oneshot
WorkingDirectory=%h
ExecStart={{.ExecCommand}}
`))

var reminderTimerTemplate = template.Must(template.New("reminder-timer").Parse(`[Unit]
Description=Timer for orbit reminder {{.Name}}

[Timer]
OnCalendar={{.Schedule}}
Persistent=true

[Install]
WantedBy=timers.target
`))

var snoozeTimerTemplate = template.Must(template.New("snooze-timer").Parse(`[Unit]
Description=Snooze timer for orbit reminder {{.Name}}

[Timer]
OnCalendar={{.OnCalendar}}
Persistent=true
Unit={{.ServiceUnit}}

[Install]
WantedBy=timers.target
`))

// GenerateTaskUnits generates units for a task.
// If schedule is empty, only a service unit is generated (no timer).
// Otherwise, both a service and timer unit are generated.
func (m *Manager) GenerateTaskUnits(name, schedule string, onMissed config.OnMissedPolicy, orbitBin string) ([]Unit, error) {
	serviceData := struct {
		Name        string
		ExecCommand string
	}{
		Name:        name,
		ExecCommand: fmt.Sprintf(`%s _run %s`, execBin(orbitBin), name),
	}

	var serviceBuf strings.Builder
	if err := serviceTemplate.Execute(&serviceBuf, serviceData); err != nil {
		return nil, fmt.Errorf("generating service unit: %w", err)
	}

	units := []Unit{
		{Name: TaskServiceName(name), Content: serviceBuf.String()},
	}

	if schedule == "" {
		return units, nil
	}

	persistent := "true"
	if onMissed == config.OnMissedSkip {
		persistent = "false"
	}

	timerData := struct {
		Name       string
		Schedule   string
		Persistent string
	}{
		Name:       name,
		Schedule:   schedule,
		Persistent: persistent,
	}

	var timerBuf strings.Builder
	if err := timerTemplate.Execute(&timerBuf, timerData); err != nil {
		return nil, fmt.Errorf("generating timer unit: %w", err)
	}

	units = append(units, Unit{Name: TaskTimerName(name), Content: timerBuf.String()})
	return units, nil
}

// GenerateReminderUnits generates service and timer units for a reminder.
// schedule is a systemd OnCalendar expression.
func (m *Manager) GenerateReminderUnits(name, schedule, orbitBin string) ([]Unit, error) {
	serviceData := struct {
		Name        string
		ExecCommand string
	}{
		Name:        name,
		ExecCommand: fmt.Sprintf(`%s _notify %s`, execBin(orbitBin), name),
	}

	var serviceBuf strings.Builder
	if err := reminderServiceTemplate.Execute(&serviceBuf, serviceData); err != nil {
		return nil, fmt.Errorf("generating reminder service unit: %w", err)
	}

	timerData := struct {
		Name     string
		Schedule string
	}{
		Name:     name,
		Schedule: schedule,
	}

	var timerBuf strings.Builder
	if err := reminderTimerTemplate.Execute(&timerBuf, timerData); err != nil {
		return nil, fmt.Errorf("generating reminder timer unit: %w", err)
	}

	return []Unit{
		{Name: ReminderServiceName(name), Content: serviceBuf.String()},
		{Name: ReminderTimerName(name), Content: timerBuf.String()},
	}, nil
}

// GenerateSnoozeTimer generates a persistent snooze timer for a reminder.
// The timer triggers the reminder's existing service unit at the specified time.
func (m *Manager) GenerateSnoozeTimer(name string, until time.Time) (Unit, error) {
	data := struct {
		Name        string
		OnCalendar  string
		ServiceUnit string
	}{
		Name:        name,
		OnCalendar:  until.Format("2006-01-02 15:04:05"),
		ServiceUnit: ReminderServiceName(name),
	}

	var buf strings.Builder
	if err := snoozeTimerTemplate.Execute(&buf, data); err != nil {
		return Unit{}, fmt.Errorf("generating snooze timer unit: %w", err)
	}

	return Unit{Name: SnoozeTimerName(name), Content: buf.String()}, nil
}

// ChangeSet describes the set of changes needed to reconcile desired state.
type ChangeSet struct {
	Create []Unit // new units to create
	Update []Unit // existing units whose content changed
	Remove []Unit // units to delete
	Keep   []Unit // units that are already up-to-date
}

// ClassifyChanges compares desired units against what's on disk and returns
// a ChangeSet. This is the foundation for a future `orbit plan` command.
func (m *Manager) ClassifyChanges(desired []Unit, existingNames []string) ChangeSet {
	systemdDir := m.UnitDir()

	existingSet := make(map[string]bool, len(existingNames))
	for _, name := range existingNames {
		existingSet[name] = true
	}

	desiredSet := make(map[string]bool, len(desired))

	var cs ChangeSet

	for _, unit := range desired {
		desiredSet[unit.Name] = true

		if !existingSet[unit.Name] {
			cs.Create = append(cs.Create, unit)
			continue
		}

		// Read existing content to check for updates
		existing, err := os.ReadFile(filepath.Join(systemdDir, unit.Name))
		if err != nil {
			// Can't read — treat as create
			cs.Create = append(cs.Create, unit)
			continue
		}

		if string(existing) != unit.Content {
			unit.OldContent = string(existing)
			cs.Update = append(cs.Update, unit)
		} else {
			cs.Keep = append(cs.Keep, unit)
		}
	}

	for _, name := range existingNames {
		if !desiredSet[name] {
			cs.Remove = append(cs.Remove, Unit{Name: name})
		}
	}

	return cs
}

// RemoveUnits stops, disables, and deletes the given units,
// then reloads the daemon once.
func (m *Manager) RemoveUnits(units []Unit) error {
	systemdDir := m.UnitDir()

	var allNames []string
	var timerNames []string
	for _, unit := range units {
		allNames = append(allNames, unit.Name)
		if strings.HasSuffix(unit.Name, ".timer") {
			timerNames = append(timerNames, unit.Name)
		}
	}

	if len(allNames) > 0 {
		m.systemctl("stop", allNames...)
	}
	if len(timerNames) > 0 {
		m.systemctl("disable", timerNames...)
	}

	for _, unit := range units {
		unitPath := filepath.Join(systemdDir, unit.Name)
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing unit file %s: %w", unit.Name, err)
		}
	}

	return m.daemonReload()
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

// ListUnits returns the names of all orbit-managed units known to systemd.
func (m *Manager) ListUnits() ([]string, error) {
	args := m.systemctlArgs("list-unit-files", "--all", "--no-legend", "--no-pager")

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

// WriteUnits creates a temporary directory, writes all unit files into it,
// and returns the directory path along with a cleanup function that removes
// the temporary directory. The caller must call cleanup when done (typically
// via defer). InstallUnits removes the directory on success, so cleanup
// becomes a no-op after a successful install.
func (m *Manager) WriteUnits(units []Unit) (tmpDir string, cleanup func(), err error) {
	systemdDir := m.UnitDir()
	if err := os.MkdirAll(systemdDir, 0755); err != nil {
		return "", nil, fmt.Errorf("creating systemd directory: %w", err)
	}
	tmpDir, err = os.MkdirTemp(systemdDir, ".orbit-staging-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp directory: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(tmpDir) }
	for _, unit := range units {
		dest := filepath.Join(tmpDir, unit.Name)
		if err := os.WriteFile(dest, []byte(unit.Content), 0644); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("writing unit file %s: %w", unit.Name, err)
		}
	}
	return tmpDir, cleanup, nil
}

// InstallUnits moves unit files from a staging directory (e.g. returned by
// WriteUnits) into the systemd user unit directory, performs a daemon-reload,
// and enables/starts any timer units. The staging directory is NOT removed
// — the caller's cleanup callback handles that.
func (m *Manager) InstallUnits(units []Unit, fromDir string) error {
	systemdDir := m.UnitDir()
	if err := os.MkdirAll(systemdDir, 0755); err != nil {
		return fmt.Errorf("creating systemd directory: %w", err)
	}

	for _, unit := range units {
		src := filepath.Join(fromDir, unit.Name)
		dst := filepath.Join(systemdDir, unit.Name)
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("installing unit file %s: %w", unit.Name, err)
		}
	}

	if err := m.daemonReload(); err != nil {
		return err
	}

	var timers []string
	for _, unit := range units {
		if strings.HasSuffix(unit.Name, ".timer") {
			timers = append(timers, unit.Name)
		}
	}
	if len(timers) > 0 {
		args := append([]string{"enable", "--now"}, timers...)
		output, err := m.systemctlOutput(args[0], args[1:]...)
		if err != nil {
			return fmt.Errorf("enabling timers: %w (output: %s)", err, output)
		}
	}

	return nil
}

// UnitDir returns the path to the user systemd unit directory.
func (m *Manager) UnitDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot determine user config directory: %v\n", err)
	}
	return filepath.Join(dir, "systemd", "user")
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

// StopAndDisableTimers stops and disables multiple timer units in batch.
func (m *Manager) StopAndDisableTimers(timerNames []string) {
	if len(timerNames) == 0 {
		return
	}
	m.systemctl("stop", timerNames...)
	m.systemctl("disable", timerNames...)
}

// EnableAndStartTimers enables and starts multiple timer units in batch.
func (m *Manager) EnableAndStartTimers(timerNames []string) {
	if len(timerNames) == 0 {
		return
	}
	args := append([]string{"--now"}, timerNames...)
	m.systemctl("enable", args...)
}
