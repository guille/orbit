package main

import (
	"os"
	"strings"
	"unicode/utf8"
)

const (
	escape = '\033'
	// spaces is sliced for padding; long enough to cover a column in one write.
	spaces = "                                                                "
)

// ANSI color codes. Disabled when NO_COLOR env is set or stdout is not a terminal.
var colorEnabled = os.Getenv("NO_COLOR") == "" && isTerminal()

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func green(s string) string {
	if !colorEnabled {
		return s
	}
	return "\033[32m" + s + "\033[0m"
}

func red(s string) string {
	if !colorEnabled {
		return s
	}
	return "\033[31m" + s + "\033[0m"
}

func yellow(s string) string {
	if !colorEnabled {
		return s
	}
	return "\033[33m" + s + "\033[0m"
}

func blue(s string) string {
	if !colorEnabled {
		return s
	}
	return "\033[34m" + s + "\033[0m"
}

func dim(s string) string {
	if !colorEnabled {
		return s
	}
	return "\033[2m" + s + "\033[0m"
}

func bold(s string) string {
	if !colorEnabled {
		return s
	}
	return "\033[1m" + s + "\033[0m"
}

// visibleLen returns the rendered width of s, ignoring ANSI escape sequences.
func visibleLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == escape {
			if end := sgrEnd(s, i); end > i {
				i = end
				continue
			}
		}
		if s[i] < utf8.RuneSelf {
			i++
		} else {
			_, size := utf8.DecodeRuneInString(s[i:])
			i += size
		}
		n++
	}
	return n
}

// sgrEnd returns the index just past the SGR sequence (ESC [ digits/semicolons m)
// starting at i, or i if s[i:] does not open one. Anything else is left visible,
// matching how the terminal would not consume it as a color change.
func sgrEnd(s string, i int) int {
	if i+1 >= len(s) || s[i+1] != '[' {
		return i
	}
	for j := i + 2; j < len(s); j++ {
		switch c := s[j]; {
		case c == 'm':
			return j + 1
		case c >= '0' && c <= '9', c == ';':
		default:
			return i
		}
	}
	return i
}

// writePadded writes a (possibly ANSI-colored) string padded to the given
// visible width.
func writePadded(b *strings.Builder, s string, width int) {
	b.WriteString(s)
	for pad := width - visibleLen(s); pad > 0; pad -= len(spaces) {
		b.WriteString(spaces[:min(pad, len(spaces))])
	}
}
