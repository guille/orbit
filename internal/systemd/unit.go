package systemd

import "strings"

// Unit represents a systemd unit file.
type Unit struct {
	Name    string
	Content string
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

// StreamIdentifier returns the SyslogIdentifier for a service unit:
// "orbit-task-foo" for "orbit-task-foo.service".
// Needed because output from a short-lived task process is often stored
// without _SYSTEMD_USER_UNIT  and invisible to `journalctl -u` (systemd#2913).
func StreamIdentifier(serviceUnit string) string {
	return strings.TrimSuffix(serviceUnit, ".service")
}

// orbitUnitGlob narrows unit listings server-side. It admits a superset of
// IsOrbitUnit, which stays the authoritative filter.
const orbitUnitGlob = "orbit-*"

// IsOrbitUnit returns true if the unit name is managed by orbit.
func IsOrbitUnit(name string) bool {
	return strings.HasPrefix(name, "orbit-task-") ||
		strings.HasPrefix(name, "orbit-reminder-") ||
		strings.HasPrefix(name, "orbit-snooze-")
}

// IsSnoozeUnit returns true if the unit name is a snooze timer.
func IsSnoozeUnit(name string) bool {
	return strings.HasPrefix(name, "orbit-snooze-")
}
