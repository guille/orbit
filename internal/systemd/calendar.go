package systemd

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// NextElapses returns the next trigger time of each given OnCalendar
// expression, keyed by expression, resolved in a single `systemd-analyze
// calendar` invocation. Expressions systemd cannot resolve are omitted.
func (m *Manager) NextElapses(schedules []string) map[string]time.Time {
	all, _ := analyzeCalendar(schedules, 1, time.Time{})
	elapses := make(map[string]time.Time, len(all))
	for schedule, times := range all {
		elapses[schedule] = times[0]
	}
	return elapses
}

// Occurrences returns up to n upcoming trigger times of an OnCalendar
// expression. Fewer than n means the schedule runs out before then; none means
// it cannot be resolved or has already ended. The error reports that
// systemd-analyze itself could not answer, which is distinct from "none".
func (m *Manager) Occurrences(schedule string, n int) ([]time.Time, error) {
	all, err := analyzeCalendar([]string{schedule}, n, time.Time{})
	return all[schedule], err
}

// NextAfter returns the first trigger time of an OnCalendar expression strictly
// after base (now, if zero). ok is false when nothing is scheduled after base;
// a non-nil error means systemd-analyze could not answer.
func (m *Manager) NextAfter(schedule string, base time.Time) (t time.Time, ok bool, err error) {
	all, err := analyzeCalendar([]string{schedule}, 1, base)
	if err != nil {
		return time.Time{}, false, err
	}
	times := all[schedule]
	if len(times) == 0 {
		return time.Time{}, false, nil
	}
	return times[0], true, nil
}

// ResolveTime parses a user-supplied point in time. It accepts a Go duration
// ("2h", that far from now), a systemd timestamp ("tomorrow", "+3d",
// "2026-09-07 17:00") or a calendar expression ("Monday", "Fri 17:00"), which
// resolves to its next occurrence. A timestamp that has already passed is
// retried as a calendar expression, so "09:00" typed in the afternoon means
// tomorrow morning; if that fails too the past instant is returned for the
// caller to reject with context.
func (m *Manager) ResolveTime(s string) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(d), nil
	}
	stamp, stampErr := analyzeTimestamp(s)
	if stampErr == nil && stamp.After(time.Now()) {
		return stamp, nil
	}
	if all, _ := analyzeCalendar([]string{s}, 1, time.Time{}); len(all[s]) > 0 {
		return all[s][0], nil
	}
	if stampErr == nil {
		return stamp, nil
	}
	return time.Time{}, fmt.Errorf("cannot parse %q as a time; use a duration (2h), a timestamp (tomorrow, 2026-09-07 17:00) or a calendar expression (Monday, Fri 17:00)", s)
}

// analyzeTimestamp resolves a systemd timestamp expression via
// `systemd-analyze timestamp`, reading the epoch it prints rather than
// re-parsing a zone-formatted string.
func analyzeTimestamp(s string) (time.Time, error) {
	output, err := exec.Command("systemd-analyze", "timestamp", s).Output()
	if err != nil {
		return time.Time{}, err
	}
	for line := range strings.SplitSeq(string(output), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "UNIX seconds" {
			continue
		}
		return parseEpoch(strings.TrimPrefix(strings.TrimSpace(value), "@"))
	}
	return time.Time{}, fmt.Errorf("no UNIX seconds in systemd-analyze output for %q", s)
}

// parseEpoch parses "1757232000" or "1757232000.165354" (fractional seconds).
func parseEpoch(s string) (time.Time, error) {
	whole, frac, _ := strings.Cut(s, ".")
	secs, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("unrecognized epoch: %s", s)
	}
	var nanos int64
	if frac != "" {
		frac = (frac + "000000000")[:9]
		if nanos, err = strconv.ParseInt(frac, 10, 64); err != nil {
			return time.Time{}, fmt.Errorf("unrecognized epoch: %s", s)
		}
	}
	return time.Unix(secs, nanos), nil
}

// analyzeCalendar runs `systemd-analyze calendar` once for every expression and
// returns their upcoming trigger times, keyed by expression. A zero base means
// now; otherwise triggers are strictly after base. Expressions systemd cannot
// resolve, or that have no trigger left, are omitted. An error is returned only
// when the tool answered for none of them (not installed, failed to start, or
// rejected the whole invocation), so callers can tell that apart from a
// schedule with nothing left.
func analyzeCalendar(schedules []string, iterations int, base time.Time) (map[string][]time.Time, error) {
	if len(schedules) == 0 {
		return nil, nil
	}

	args := []string{"calendar", "--iterations=" + strconv.Itoa(iterations)}
	if !base.IsZero() {
		args = append(args, fmt.Sprintf("--base-time=@%d.%06d", base.Unix(), base.Nanosecond()/1000))
	}
	args = append(args, schedules...)
	// A rejected expression is reported on stderr and simply yields no block on
	// stdout, so a non-zero exit says nothing about the ones that did resolve.
	output, runErr := exec.Command("systemd-analyze", args...).Output()

	// systemd-analyze prints one property block per expression, separated by a
	// blank line. "Original form" echoes the expression as given, and is
	// omitted when the expression is already in normalized form. The first
	// trigger is "Next elapse" and later ones "Iteration #N"; a schedule with
	// nothing left says "Next elapse: never".
	result := make(map[string][]time.Time, len(schedules))
	answered := false
	for block := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n\n") {
		var schedule string
		var times []time.Time
		for line := range strings.SplitSeq(block, "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			switch {
			case key == "Original form":
				schedule = value
			case key == "Normalized form":
				if schedule == "" {
					schedule = value
				}
			case key == "Next elapse" || strings.HasPrefix(key, "Iteration #"):
				if t, err := parseCalendarTime(value); err == nil {
					times = append(times, t)
				}
			}
		}
		if schedule != "" && len(times) > 0 {
			result[schedule] = times
		}
		if schedule != "" {
			answered = true
		}
	}
	if runErr != nil && !answered {
		return nil, fmt.Errorf("systemd-analyze calendar: %w", runErr)
	}
	return result, nil
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
