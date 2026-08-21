package main

import (
	"strings"
)

// columnGap separates rendered table columns.
const columnGap = "  "

// table renders rows as aligned columns, sizing each to its widest cell and
// ignoring ANSI escapes when measuring width.
type table struct {
	headers []string
	rows    [][]string
}

func newTable(headers ...string) *table {
	return &table{headers: headers}
}

func (t *table) add(cells ...string) {
	t.rows = append(t.rows, cells)
}

func (t *table) String() string {
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = visibleLen(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			if i < len(widths) {
				widths[i] = max(widths[i], visibleLen(c))
			}
		}
	}

	rule := make([]string, len(t.headers))
	for i, h := range t.headers {
		rule[i] = strings.Repeat("-", visibleLen(h))
	}

	var b strings.Builder
	writeCells(&b, t.headers, widths)
	writeCells(&b, rule, widths)
	for _, r := range t.rows {
		writeCells(&b, r, widths)
	}
	return b.String()
}

// writeCells writes one row, padding every cell but the last to its column width.
func writeCells(b *strings.Builder, cells []string, widths []int) {
	for i, c := range cells {
		if i > 0 {
			b.WriteString(columnGap)
		}
		if i == len(cells)-1 {
			b.WriteString(c)
			break
		}
		writePadded(b, c, widths[i])
	}
	b.WriteByte('\n')
}
