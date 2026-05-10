// Package picker provides a simple interactive terminal picker.
package picker

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// ErrCancelled is returned when the user cancels the picker.
var ErrCancelled = fmt.Errorf("selection cancelled")

// Pick displays an interactive picker and returns the selected item.
// Returns ErrCancelled if the user presses Ctrl+C.
// Returns an error if stdin is not a terminal.
func Pick(prompt string, items []string) (string, error) {
	if len(items) == 0 {
		return "", fmt.Errorf("no items to select from")
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("not a terminal")
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return "", fmt.Errorf("enabling raw mode: %w", err)
	}
	//nolint:errcheck
	defer term.Restore(fd, oldState)

	// Restore terminal on signals that would otherwise leave it raw.
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	defer close(done)
	go func() {
		select {
		case <-sigCh:
			//nolint:errcheck
			term.Restore(fd, oldState)
			os.Exit(1)
		case <-done:
		}
	}()

	cursor := 0
	filter := ""
	filterPos := 0
	prevLines := 0

	for {
		filtered := matchItems(items, filter)
		if cursor >= len(filtered) {
			cursor = max(0, len(filtered)-1)
		}

		prevLines = render(prompt, filtered, cursor, filter, filterPos, prevLines)

		key, err := readKey(fd)
		if err != nil {
			return "", err
		}

		switch key {
		case keyUp:
			if cursor > 0 {
				cursor--
			}
		case keyDown:
			if cursor < len(filtered)-1 {
				cursor++
			}
		case keyLeft:
			if filterPos > 0 {
				filterPos--
			}
		case keyRight:
			if filterPos < len(filter) {
				filterPos++
			}
		case keyEnter:
			if len(filtered) > 0 {
				clearLines(prevLines)
				return filtered[cursor], nil
			}
		case keyCtrlC:
			clearLines(prevLines)
			return "", ErrCancelled
		case keyEsc:
			filter = ""
			filterPos = 0
			cursor = 0
		case keyBackspace:
			if filterPos > 0 {
				filter = filter[:filterPos-1] + filter[filterPos:]
				filterPos--
				cursor = 0
			}
		case keyDelete:
			if filterPos < len(filter) {
				filter = filter[:filterPos] + filter[filterPos+1:]
				cursor = 0
			}
		default:
			if key >= 0x20 && key < 0x7f {
				filter = filter[:filterPos] + string(rune(key)) + filter[filterPos:]
				filterPos++
				cursor = 0
			}
		}
	}
}

const (
	keyUp        = -1
	keyDown      = -2
	keyEnter     = -3
	keyCtrlC     = -4
	keyEsc       = -5
	keyBackspace = -6
	keyLeft      = -7
	keyRight     = -8
	keyDelete    = -9
	keyUnknown   = -10
)

// readKey reads a single keypress from the terminal.
// Handles the Esc/arrow ambiguity by doing a short timeout read after 0x1b.
func readKey(fd int) (int, error) {
	var buf [1]byte
	if _, err := os.Stdin.Read(buf[:]); err != nil {
		return 0, err
	}

	switch buf[0] {
	case 3:
		return keyCtrlC, nil
	case 13:
		return keyEnter, nil
	case 127, 8:
		return keyBackspace, nil
	case 27: // Esc — might be start of escape sequence
		return readEscapeSeq(fd)
	default:
		return int(buf[0]), nil
	}
}

// readEscapeSeq reads additional bytes after 0x1b with a short timeout.
// If no bytes arrive within 50ms, it's a standalone Esc.
func readEscapeSeq(fd int) (int, error) {
	// Set a short read timeout
	ready, err := waitForInput(fd, 50*time.Millisecond)
	if err != nil || !ready {
		return keyEsc, nil
	}

	var buf [2]byte
	n, err := os.Stdin.Read(buf[:])
	if err != nil || n == 0 {
		return keyEsc, nil
	}

	if n >= 2 && buf[0] == '[' {
		switch buf[1] {
		case 'A':
			return keyUp, nil
		case 'B':
			return keyDown, nil
		case 'C':
			return keyRight, nil
		case 'D':
			return keyLeft, nil
		case '3': // Delete key: \x1b[3~
			// Read the trailing '~'
			var tilde [1]byte
			if _, err := os.Stdin.Read(tilde[:]); err == nil && tilde[0] == '~' {
				return keyDelete, nil
			}
		}
	}

	return keyUnknown, nil
}

// waitForInput uses select(2) to wait for input with a timeout.
func waitForInput(fd int, timeout time.Duration) (bool, error) {
	var readfds syscall.FdSet
	readfds.Bits[fd/64] |= 1 << (uint(fd) % 64)
	tv := syscall.Timeval{
		Sec:  int64(timeout / time.Second),
		Usec: int64((timeout % time.Second) / time.Microsecond),
	}
	n, err := syscall.Select(fd+1, &readfds, nil, nil, &tv)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// matchItems returns items that fuzzy-match the filter (case-insensitive).
func matchItems(items []string, filter string) []string {
	if filter == "" {
		result := make([]string, len(items))
		copy(result, items)
		return result
	}
	var result []string
	for _, item := range items {
		if fuzzyMatch(item, filter) != nil {
			result = append(result, item)
		}
	}
	return result
}

// fuzzyMatch returns the byte indices of matched characters in item for the
// given filter, or nil if there's no match. Matches leftmost positions.
//
// NOTE: This operates on bytes, not runes. This is safe because filter input
// is restricted to printable ASCII (0x20-0x7e) in Pick(). If non-ASCII input
// is ever allowed, this must be rewritten to use rune iteration, and
// highlightMatch (which iterates runes) must be updated to match.
func fuzzyMatch(item, filter string) []int {
	lowerItem := strings.ToLower(item)
	lowerFilter := strings.ToLower(filter)

	indices := make([]int, 0, len(lowerFilter))
	j := 0
	for i := 0; i < len(lowerItem) && j < len(lowerFilter); i++ {
		if lowerItem[i] == lowerFilter[j] {
			indices = append(indices, i)
			j++
		}
	}
	if j < len(lowerFilter) {
		return nil
	}
	return indices
}

// termHeight returns the terminal height, or 24 as a fallback.
func termHeight() int {
	_, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || height == 0 {
		return 24
	}
	return height
}

// render draws the picker UI. Returns the number of lines rendered.
// After render, cursor is on the filter line (second-to-last line).
func render(prompt string, items []string, cursor int, filter string, filterPos int, prevLines int) int {
	// Determine max visible items based on terminal height.
	// Reserve lines for: prompt(1) + filter(1) + help(1) + 1 margin = 4
	maxItems := max(termHeight()-4, 1)

	// Compute visible window around cursor
	visibleItems := items
	offset := 0
	if len(items) > maxItems {
		// Keep cursor visible within the window
		start := max(cursor-maxItems/2, 0)
		if start+maxItems > len(items) {
			start = len(items) - maxItems
		}
		visibleItems = items[start : start+maxItems]
		offset = start
	}

	// Build the frame into a buffer to reduce flicker
	var buf strings.Builder

	if prevLines > 0 {
		moveUpAndClear(&buf, prevLines)
	}

	nLines := 1
	fmt.Fprintf(&buf, "\033[1m%s\033[0m\r\n", prompt)

	if len(visibleItems) == 0 {
		buf.WriteString("  \033[2m(no matches)\033[0m\r\n")
		nLines++
	} else {
		// Show scroll indicator if list is truncated
		if offset > 0 {
			buf.WriteString("  \033[2m...\033[0m\r\n")
			nLines++
		}
		for i, item := range visibleItems {
			display := highlightMatch(item, filter)
			if i+offset == cursor {
				fmt.Fprintf(&buf, "  \033[36m> %s\033[0m\r\n", display)
			} else {
				fmt.Fprintf(&buf, "    %s\r\n", display)
			}
			nLines++
		}
		if offset+len(visibleItems) < len(items) {
			buf.WriteString("  \033[2m...\033[0m\r\n")
			nLines++
		}
	}

	// Filter line
	filterPrefix := "  /"
	fmt.Fprintf(&buf, "%s%s\r\n", filterPrefix, filter)
	nLines++

	// Help line (last)
	buf.WriteString("  \033[2m\033[1mEnter\033[22m select  \033[1mEsc\033[22m clear filter\033[0m")
	nLines++

	// Move cursor back to filter line
	buf.WriteString("\033[A")
	fmt.Fprintf(&buf, "\r\033[%dC", len(filterPrefix)+filterPos)

	//nolint:errcheck
	// Write entire frame at once
	os.Stdout.WriteString(buf.String())

	return nLines
}

// highlightMatch underlines the characters in item that match the fuzzy filter.
// Uses underline (\033[4m) rather than bold to avoid interaction with
// surrounding color escapes (e.g. cyan for the selected item).
func highlightMatch(item, filter string) string {
	if filter == "" {
		return item
	}
	indices := fuzzyMatch(item, filter)
	if indices == nil {
		return item
	}

	matchSet := make(map[int]bool, len(indices))
	for _, idx := range indices {
		matchSet[idx] = true
	}

	var b strings.Builder
	inHighlight := false
	for i, ch := range item {
		if matchSet[i] && !inHighlight {
			b.WriteString("\033[4m")
			inHighlight = true
		} else if !matchSet[i] && inHighlight {
			b.WriteString("\033[24m")
			inHighlight = false
		}
		b.WriteRune(ch)
	}
	if inHighlight {
		b.WriteString("\033[24m")
	}
	return b.String()
}

// moveUpAndClear moves the cursor from the filter line (second-to-last)
// to the top of the picker output and erases everything below.
func moveUpAndClear(buf *strings.Builder, lines int) {
	up := lines - 2 // cursor is on filter line
	if up > 0 {
		fmt.Fprintf(buf, "\033[%dA", up)
	}
	buf.WriteString("\r\033[J")
}

// clearLines erases picker output. Cursor is on filter line (second-to-last).
func clearLines(lines int) {
	if lines > 0 {
		var buf strings.Builder
		moveUpAndClear(&buf, lines)
		//nolint:errcheck
		os.Stdout.WriteString(buf.String())
	}
}
