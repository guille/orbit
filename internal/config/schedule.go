package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Schedule is one or more systemd OnCalendar expressions; a timer fires on the
// union of them. TOML and JSON accept a bare string or an array of strings. A
// single expression marshals back as a bare string and an empty Schedule as
// "", so state files written before arrays were accepted stay byte-identical.
type Schedule []string

// IsZero reports whether no expression is set.
func (s Schedule) IsZero() bool { return len(s) == 0 }

// String joins the expressions for display. The separator is one that cannot
// occur inside an OnCalendar expression, so "09:00 | 13:30" is never mistaken
// for the single expression "09:00,13:30", which systemd reads as minutes 00
// and 13 with second 30.
func (s Schedule) String() string { return strings.Join(s, " | ") }

// duplicate returns the first expression that appears more than once.
func (s Schedule) duplicate() (string, bool) {
	for i, expr := range s {
		if slices.Contains(s[i+1:], expr) {
			return expr, true
		}
	}
	return "", false
}

// UnmarshalTOML implements toml.Unmarshaler.
func (s *Schedule) UnmarshalTOML(data any) error { return s.decode(data) }

// UnmarshalJSON implements json.Unmarshaler.
func (s *Schedule) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	return s.decode(v)
}

// MarshalJSON implements json.Marshaler.
func (s Schedule) MarshalJSON() ([]byte, error) {
	switch len(s) {
	case 0:
		return json.Marshal("")
	case 1:
		return json.Marshal(s[0])
	default:
		return json.Marshal([]string(s))
	}
}

// decode accepts the generic value both decoders produce: a string, a []any
// of strings, or nil. An empty string means no schedule, like an absent key.
func (s *Schedule) decode(v any) error {
	switch v := v.(type) {
	case nil:
		*s = nil
	case string:
		if v == "" {
			*s = nil
		} else {
			*s = Schedule{v}
		}
	case []any:
		out := make(Schedule, 0, len(v))
		for _, e := range v {
			expr, ok := e.(string)
			if !ok {
				return fmt.Errorf("schedule: array element %v is not a string", e)
			}
			if expr == "" {
				return fmt.Errorf("schedule: array contains an empty expression")
			}
			out = append(out, expr)
		}
		if len(out) == 0 {
			out = nil
		}
		*s = out
	default:
		return fmt.Errorf("schedule must be a string or an array of strings, got %T", v)
	}
	return nil
}
