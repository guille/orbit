// Package notify sends desktop notifications via notify-send.
package notify

import (
	"fmt"
	"os/exec"
	"strings"
)

// Binary is the notification command orbit depends on.
const Binary = "notify-send"

// Notification is a desktop notification to display. An empty Urgency
// leaves the level to the notification server.
type Notification struct {
	Title, Body, Icon, Urgency string
}

// Send fires a desktop notification via notify-send.
func Send(n Notification) error {
	out, err := exec.Command(Binary, args(n)...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %w: %s", Binary, err, msg)
		}
		return fmt.Errorf("%s: %w", Binary, err)
	}
	return nil
}

// args builds the notify-send argv.
func args(n Notification) []string {
	var a []string
	if n.Icon != "" {
		a = append(a, "--icon", n.Icon)
	}
	if n.Urgency != "" {
		a = append(a, "--urgency", n.Urgency)
	}
	// "--" keeps a message starting with "-" from being parsed as an option.
	return append(a, "--", n.Title, n.Body)
}
