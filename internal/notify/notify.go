// Package notify sends desktop notifications via notify-send.
package notify

import (
	"fmt"
	"os/exec"
	"strings"
)

// Binary is the notification command orbit depends on.
const Binary = "notify-send"

// Notification is a desktop notification to display.
type Notification struct {
	Title, Body, Icon string
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
	return append(a, n.Title, n.Body)
}
