package main

import "testing"

func TestTableSizesColumnsToContent(t *testing.T) {
	tbl := newTable("NAME", "STATUS")
	tbl.add("a-very-long-entry-name", "ok")
	tbl.add("short", "new")

	want := "NAME                    STATUS\n" +
		"----                    ------\n" +
		"a-very-long-entry-name  ok\n" +
		"short                   new\n"

	if got := tbl.String(); got != want {
		t.Errorf("table.String() =\n%q\nwant\n%q", got, want)
	}
}

// Colored cells must be measured by visible width, not byte length, or every
// column after a colored one drifts.
func TestTableIgnoresANSIWhenSizing(t *testing.T) {
	colorEnabled = true
	t.Cleanup(func() { colorEnabled = false })

	tbl := newTable("NAME", "STATUS", "NEXT RUN")
	tbl.add("backup", green("ok"), "in 2h")
	tbl.add("sync", red("failed (2)"), "in 5m")

	want := "NAME    STATUS      NEXT RUN\n" +
		"----    ------      --------\n" +
		"backup  " + green("ok") + "          in 2h\n" +
		"sync    " + red("failed (2)") + "  in 5m\n"

	if got := tbl.String(); got != want {
		t.Errorf("table.String() =\n%q\nwant\n%q", got, want)
	}
}

func TestVisibleLen(t *testing.T) {
	colorEnabled = true
	t.Cleanup(func() { colorEnabled = false })

	tests := []struct {
		in   string
		want int
	}{
		{"plain", 5},
		{green("ok"), 2},
		{dim(cellDisabled), len(cellDisabled)},
		{"café", 4}, // multi-byte runes count once
		{"", 0},
	}

	for _, tc := range tests {
		if got := visibleLen(tc.in); got != tc.want {
			t.Errorf("visibleLen(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
