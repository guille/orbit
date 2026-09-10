package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestSchedule_TOMLForms(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.one]
command  = "echo"
schedule = "daily"

[tasks.many]
command  = "echo"
schedule = ["Mon..Fri 09:00", "Mon..Fri 13:30"]

[tasks.none]
command  = "echo"
schedule = []

[tasks.blank]
command  = "echo"
schedule = ""

[tasks.manual]
command  = "echo"
`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Schedule{
		"one":    {"daily"},
		"many":   {"Mon..Fri 09:00", "Mon..Fri 13:30"},
		"none":   nil,
		"blank":  nil,
		"manual": nil,
	}
	for name, w := range want {
		if got := cfg.Tasks[name].Schedule; !slices.Equal(got, w) {
			t.Errorf("%s: schedule = %v, want %v", name, got, w)
		}
	}
	if cfg.Tasks["none"].OnMissed != "" {
		t.Error("an empty list is a manual task and gets no on_missed default")
	}
}

func TestSchedule_TOMLBadTypes(t *testing.T) {
	for _, tc := range []struct{ name, toml, want string }{
		{"number", `schedule = 5`, "string or an array"},
		{"mixed array", `schedule = ["daily", 3]`, "not a string"},
		{"empty element", `schedule = ["daily", ""]`, "empty expression"},
	} {
		_, err := loadFromString("[tasks.t]\ncommand = \"echo\"\n" + tc.toml)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

func TestValidate_ScheduleList(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.standup]
command  = "echo"
schedule = ["Mon..Fri 09:00", "Mon..Fri 13:30"]

[reminders.r]
message  = "hi"
schedule = ["daily", "Sat 10:00"]
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_ScheduleListDuplicate(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.t]
command  = "echo"
schedule = ["daily", "weekly", "daily"]
`)
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), `task t: schedule lists "daily" more than once`) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidate_ScheduleListBadMember(t *testing.T) {
	cfg, err := loadFromString(`
[tasks.t]
command  = "echo"
schedule = ["daily", "not a schedule"]
`)
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), `task t: invalid schedule "not a schedule"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidate_EmptyScheduleListReminder(t *testing.T) {
	cfg, err := loadFromString(`
[reminders.r]
message  = "hi"
schedule = []
`)
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "schedule is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestSchedule_JSON(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Schedule
		out  string
	}{
		{`""`, nil, `""`},
		{`null`, nil, `""`},
		{`"daily"`, Schedule{"daily"}, `"daily"`},
		{`["daily"]`, Schedule{"daily"}, `"daily"`},
		{`["a","b"]`, Schedule{"a", "b"}, `["a","b"]`},
	} {
		var s Schedule
		if err := json.Unmarshal([]byte(tc.in), &s); err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if !slices.Equal(s, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.in, s, tc.want)
		}
		out, err := json.Marshal(s)
		if err != nil || string(out) != tc.out {
			t.Errorf("%s: marshals to %s (%v), want %s", tc.in, out, err, tc.out)
		}
	}
}

func TestSchedule_String(t *testing.T) {
	if got := (Schedule{"09:00", "13:30"}).String(); got != "09:00 | 13:30" {
		t.Errorf("got %q", got)
	}
	if got := (Schedule)(nil).String(); got != "" {
		t.Errorf("got %q", got)
	}
}
