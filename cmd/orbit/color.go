package main

import (
	"fmt"
	"os"
	"regexp"
	"unicode/utf8"
)

// ANSI color codes. Disabled when NO_COLOR env is set or stdout is not a terminal.
var (
	colorEnabled = os.Getenv("NO_COLOR") == "" && isTerminal()
	ansiRe       = regexp.MustCompile(`\033\[[0-9;]*m`)
)

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
	return utf8.RuneCountInString(ansiRe.ReplaceAllString(s, ""))
}

// padRight pads a (possibly ANSI-colored) string to the given visible width.
func padRight(s string, width int) string {
	visible := visibleLen(s)
	if visible >= width {
		return s
	}
	return fmt.Sprintf("%s%*s", s, width-visible, "")
}
