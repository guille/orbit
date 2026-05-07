package picker

import "testing"

func TestMatchItems(t *testing.T) {
	items := []string{"backup", "deploy", "build-docs", "database-migrate"}

	tests := []struct {
		filter string
		want   []string
	}{
		{"", items},
		{"ba", []string{"backup", "database-migrate"}},
		{"BA", []string{"backup", "database-migrate"}}, // case insensitive
		{"deploy", []string{"deploy"}},
		{"xyz", nil},
		{"build", []string{"build-docs"}},
		// Fuzzy matching
		{"bld", []string{"build-docs"}},       // b..l..d
		{"dold", nil},                         // no item matches d..o..l..d
		{"dbm", []string{"database-migrate"}}, // d..b..m
		{"bp", []string{"backup"}},            // b..p (fuzzy)
	}

	for _, tt := range tests {
		got := matchItems(items, tt.filter)
		if len(got) != len(tt.want) {
			t.Errorf("matchItems(%q): got %v (%d items), want %v (%d items)", tt.filter, got, len(got), tt.want, len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("matchItems(%q)[%d]: got %q, want %q", tt.filter, i, got[i], tt.want[i])
			}
		}
	}
}

func TestFuzzyMatch(t *testing.T) {
	tests := []struct {
		item   string
		filter string
		want   []int // nil means no match
	}{
		{"backup", "ba", []int{0, 1}},
		{"backup", "bp", []int{0, 5}},
		{"docker-build", "dold", []int{0, 1, 10, 11}}, // d(0) o(1) l(10) d(11)
		{"docker-build", "dbd", []int{0, 7, 11}},
		{"database-migrate", "dbm", []int{0, 4, 9}},
		{"abc", "abc", []int{0, 1, 2}},
		{"abc", "xyz", nil},
		{"ABC", "abc", []int{0, 1, 2}}, // case insensitive
	}

	for _, tt := range tests {
		got := fuzzyMatch(tt.item, tt.filter)
		if tt.want == nil {
			if got != nil {
				t.Errorf("fuzzyMatch(%q, %q): expected nil, got %v", tt.item, tt.filter, got)
			}
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("fuzzyMatch(%q, %q): got %v, want %v", tt.item, tt.filter, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("fuzzyMatch(%q, %q)[%d]: got %d, want %d", tt.item, tt.filter, i, got[i], tt.want[i])
			}
		}
	}
}

func TestMatchItems_Empty(t *testing.T) {
	got := matchItems(nil, "test")
	if len(got) != 0 {
		t.Errorf("expected empty result for nil items, got %d", len(got))
	}
}

func TestMatchItems_SingleItem(t *testing.T) {
	got := matchItems([]string{"only"}, "")
	if len(got) != 1 || got[0] != "only" {
		t.Errorf("expected [only], got %v", got)
	}
	got = matchItems([]string{"only"}, "on")
	if len(got) != 1 || got[0] != "only" {
		t.Errorf("expected [only] for filter 'on', got %v", got)
	}
	got = matchItems([]string{"only"}, "xyz")
	if len(got) != 0 {
		t.Errorf("expected [] for filter 'xyz', got %v", got)
	}
}

func TestMatchItems_NoCopyAliasing(t *testing.T) {
	items := []string{"a", "b", "c"}
	got := matchItems(items, "")
	got[0] = "mutated"
	if items[0] == "mutated" {
		t.Error("matchItems returned aliased slice; mutation affected original")
	}
}

func TestHighlightMatch(t *testing.T) {
	tests := []struct {
		item   string
		filter string
		want   string
	}{
		// No filter — return as-is
		{"backup", "", "backup"},
		// No match — return as-is
		{"backup", "xyz", "backup"},
		// Consecutive match at start
		{"backup", "ba", "\033[4mba\033[24mckup"},
		// Non-consecutive match
		{"backup", "bp", "\033[4mb\033[24macku\033[4mp\033[24m"},
		// Full match
		{"abc", "abc", "\033[4mabc\033[24m"},
		// Single char
		{"backup", "k", "bac\033[4mk\033[24mup"},
	}
	for _, tt := range tests {
		got := highlightMatch(tt.item, tt.filter)
		if got != tt.want {
			t.Errorf("highlightMatch(%q, %q):\n  got  %q\n  want %q", tt.item, tt.filter, got, tt.want)
		}
	}
}
