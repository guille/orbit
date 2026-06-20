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
	systemdDir := m.unitDir()

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

// ApplyUnits writes all units to disk, reloads the daemon once,
// then enables and starts any timer units.
func (m *Manager) ApplyUnits(units []Unit) error {
	systemdDir := m.unitDir()
	if err := os.MkdirAll(systemdDir, 0755); err != nil {
		return fmt.Errorf("creating systemd directory: %w", err)
	}

	for _, unit := range units {
		dest := filepath.Join(systemdDir, unit.Name)
		if err := os.WriteFile(dest, []byte(unit.Content), 0644); err != nil {
			return fmt.Errorf("writing unit file %s: %w", unit.Name, err)
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

// RemoveUnits stops, disables, and deletes the given units,
// then reloads the daemon once.
func (m *Manager) RemoveUnits(units []Unit) error {
	systemdDir := m.unitDir()

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

// unitDir returns the path to the user systemd unit directory.
func (m *Manager) unitDir() string {
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
