package systemd

import (
	"os/exec"
	"testing"
	"time"
)

// A schedule with exactly five firings, all in the future, so tests are
// deterministic without depending on the current date.
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

func TestOccurrences(t *testing.T) {
	requireSystemdAnalyze(t)
	m := NewManager()

	t.Run("short schedule yields fewer than asked, exit status notwithstanding", func(t *testing.T) {
		got, err := m.Occurrences([]string{fiveDays}, 8)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 5 {
			t.Fatalf("got %d occurrences, want 5: %v", len(got), got)
		}
		for i, want := range []time.Time{jan2030(1), jan2030(2), jan2030(3), jan2030(4), jan2030(5)} {
			if !got[i].Equal(want) {
				t.Errorf("occurrence %d = %v, want %v", i, got[i], want)
			}
		}
	})

	t.Run("asks for fewer than exist", func(t *testing.T) {
		if got, _ := m.Occurrences([]string{fiveDays}, 2); len(got) != 2 || !got[1].Equal(jan2030(2)) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("ended schedule", func(t *testing.T) {
		got, err := m.Occurrences([]string{"2020-01-01 09:00"}, 3)
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v; want none and no error", got, err)
		}
	})

	t.Run("unresolvable", func(t *testing.T) {
		// Nothing answered at all reads as a failure to answer: callers pass
		// schedules that apply already validated, so this only happens when
		// the tool itself is broken.
		got, err := m.Occurrences([]string{"not a schedule"}, 3)
		if err == nil || len(got) != 0 {
			t.Fatalf("got %v, %v; want none and an error", got, err)
		}
	})

	t.Run("open-ended schedule is strictly increasing", func(t *testing.T) {
		got, _ := m.Occurrences([]string{"daily"}, 3)
		if len(got) != 3 {
			t.Fatalf("got %d occurrences, want 3", len(got))
		}
		for i := 1; i < len(got); i++ {
			if !got[i].After(got[i-1]) {
				t.Errorf("occurrence %d (%v) not after %d (%v)", i, got[i], i-1, got[i-1])
			}
		}
	})
}

func TestAnalyzeCalendarToolFailure(t *testing.T) {
	// An empty PATH makes systemd-analyze unrunnable; that must surface as an
	// error rather than look like a schedule with nothing left.
	t.Setenv("PATH", t.TempDir())
	if _, err := analyzeCalendar([]string{"daily"}, 1, time.Time{}); err == nil {
		t.Fatal("expected an error when systemd-analyze cannot run")
	}
}

func TestNextElapses(t *testing.T) {
	requireSystemdAnalyze(t)

	got, _ := NewManager().NextElapses([]string{fiveDays, "daily", "bogus"}, time.Time{})
	if !got[fiveDays].Equal(jan2030(1)) {
		t.Errorf("%q = %v, want %v", fiveDays, got[fiveDays], jan2030(1))
	}
	if _, ok := got["daily"]; !ok {
		t.Error("daily missing")
	}
	if _, ok := got["bogus"]; ok {
		t.Error("unresolvable expression should be omitted")
	}
}

func TestNextElapses_Base(t *testing.T) {
	requireSystemdAnalyze(t)
	m := NewManager()

	tests := []struct {
		name string
		base time.Time
		want time.Time
		ok   bool
	}{
		{"base equal to an occurrence is exclusive", jan2030(1), jan2030(2), true},
		{"base just before", jan2030(1).Add(-time.Second), jan2030(1), true},
		{"base between", jan2030(2).Add(13 * time.Hour), jan2030(3), true},
		{"base at last occurrence", jan2030(5), time.Time{}, false},
	}
	for _, tc := range tests {
		elapses, err := m.NextElapses([]string{fiveDays, "bogus"}, tc.base)
		if err != nil {
			t.Fatal(err)
		}
		// An ended expression is answered with the zero time; a rejected one
		// is absent, so callers can tell the two apart.
		got, answered := elapses[fiveDays]
		if !answered || !got.Equal(tc.want) || got.IsZero() != !tc.ok {
			t.Errorf("%s: NextElapses(%v) = %v, %v; want %v", tc.name, tc.base, got, answered, tc.want)
		}
		if _, ok := elapses["bogus"]; ok {
			t.Errorf("%s: rejected expression should be absent", tc.name)
		}
	}
}

func TestOccurrences_Merge(t *testing.T) {
	requireSystemdAnalyze(t)
	m := NewManager()
	// Three afternoons interleaved with the five mornings, plus one exact
	// duplicate of a morning firing.
	afternoons := "2030-01-02..04 15:00"
	dupMorning := "2030-01-03 09:00"
	at := func(day, hour int) time.Time {
		return time.Date(2030, time.January, day, hour, 0, 0, 0, time.Local)
	}

	t.Run("interleaved, deduped, truncated", func(t *testing.T) {
		got, err := m.Occurrences([]string{fiveDays, afternoons, dupMorning}, 5)
		if err != nil {
			t.Fatal(err)
		}
		want := []time.Time{at(1, 9), at(2, 9), at(2, 15), at(3, 9), at(3, 15)}
		if len(got) != len(want) {
			t.Fatalf("got %d occurrences, want %d: %v", len(got), len(want), got)
		}
		for i := range want {
			if !got[i].Equal(want[i]) {
				t.Errorf("occurrence %d = %v, want %v", i, got[i], want[i])
			}
		}
	})

	t.Run("union runs out", func(t *testing.T) {
		got, err := m.Occurrences([]string{fiveDays, afternoons}, 20)
		if err != nil || len(got) != 8 {
			t.Fatalf("got %d occurrences, %v; want 8 and no error", len(got), err)
		}
	})

	t.Run("one ended member does not lose the other", func(t *testing.T) {
		got, err := m.Occurrences([]string{"2020-01-01 09:00", fiveDays}, 2)
		if err != nil || len(got) != 2 || !got[0].Equal(jan2030(1)) {
			t.Fatalf("got %v, %v", got, err)
		}
	})
}

func TestResolveTime(t *testing.T) {
	requireSystemdAnalyze(t)
	m := NewManager()
	now := time.Now()
	tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.Local)

	t.Run("duration", func(t *testing.T) {
		got, err := m.ResolveTime("2h")
		if err != nil {
			t.Fatal(err)
		}
		if d := got.Sub(now.Add(2 * time.Hour)); d < -5*time.Second || d > 5*time.Second {
			t.Errorf("got %v, want about %v", got, now.Add(2*time.Hour))
		}
	})

	t.Run("timestamp", func(t *testing.T) {
		got, err := m.ResolveTime("tomorrow")
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(tomorrow) {
			t.Errorf("got %v, want %v", got, tomorrow)
		}
		got, err = m.ResolveTime("2030-01-01 09:00")
		if err != nil || !got.Equal(jan2030(1)) {
			t.Errorf("got %v, %v; want %v", got, err, jan2030(1))
		}
	})

	t.Run("calendar expression resolves to next occurrence", func(t *testing.T) {
		got, err := m.ResolveTime("Monday")
		if err != nil {
			t.Fatal(err)
		}
		if got.Weekday() != time.Monday || got.Hour() != 0 || !got.After(now) {
			t.Errorf("got %v, want a future Monday at 00:00", got)
		}
	})

	t.Run("past clock time falls through to calendar", func(t *testing.T) {
		// As a timestamp "00:00" is today's midnight, already past; as a
		// calendar expression it is the coming midnight.
		got, err := m.ResolveTime("00:00")
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(tomorrow) {
			t.Errorf("got %v, want %v", got, tomorrow)
		}
	})

	t.Run("past timestamp is returned for the caller to reject", func(t *testing.T) {
		got, err := m.ResolveTime("1h ago")
		if err != nil {
			t.Fatal(err)
		}
		if !got.Before(now) {
			t.Errorf("got %v, want a past instant", got)
		}
	})

	for _, bad := range []string{"next friday", "3 days", ""} {
		if got, err := m.ResolveTime(bad); err == nil {
			t.Errorf("ResolveTime(%q) = %v, want error", bad, got)
		}
	}
}

func TestParseEpoch(t *testing.T) {
	tests := []struct {
		in   string
		want time.Time
	}{
		{"1757232000", time.Unix(1757232000, 0)},
		{"1757232000.5", time.Unix(1757232000, 500_000_000)},
		{"1757232000.165354", time.Unix(1757232000, 165_354_000)},
	}
	for _, tc := range tests {
		got, err := parseEpoch(tc.in)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("parseEpoch(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	if _, err := parseEpoch("abc"); err == nil {
		t.Error("expected error for non-numeric epoch")
	}
}
