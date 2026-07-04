package systemd

import (
	"fmt"
	"strings"
	"text/template"
	"time"

	"go.guillerg.dev/orbit/internal/config"
)

// execBin returns the quoted command token for ExecStart.
// Empty defaults to bare "orbit".
func execBin(orbitBin string) string {
	if orbitBin == "" {
		orbitBin = "orbit"
	}
	return fmt.Sprintf("%q", orbitBin)
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
func GenerateTaskUnits(name, schedule string, onMissed config.OnMissedPolicy, orbitBin string) ([]Unit, error) {
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
func GenerateReminderUnits(name, schedule, orbitBin string) ([]Unit, error) {
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
func GenerateSnoozeTimer(name string, until time.Time) (Unit, error) {
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
