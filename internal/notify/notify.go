// Package notify sends desktop notifications via notify-send.
package notify

import "os/exec"

// Notification is a desktop notification to display.
type Notification struct {
	Title, Body, Icon string
}

// Send fires a desktop notification via notify-send.
func Send(n Notification) error {
	return exec.Command("notify-send", args(n)...).Run()
}

// args builds the notify-send argv.
func args(n Notification) []string {
	var a []string
	if n.Icon != "" {
		a = append(a, "--icon", n.Icon)
	}
	return append(a, n.Title, n.Body)
}
