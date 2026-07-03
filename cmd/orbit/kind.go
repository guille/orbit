package main

import (
	"fmt"

	"go.guillerg.dev/orbit/internal/state"
)

// entryKind distinguishes tasks from reminders in CLI prompts, error text, and
// the apply change set.
type entryKind string

const (
	kindTask     entryKind = "task"
	kindReminder entryKind = "reminder"
)

// rejectWrongKind returns an error if args[0] names an entry of a kind other
// than want (e.g. asking to run a reminder). Names shared by both kinds pass.
func rejectWrongKind(stateStore *state.State, args []string, want entryKind) error {
	if len(args) == 0 {
		return nil
	}
	name := args[0]
	isTask, isReminder := stateStore.GetAppliedConfig().Classify(name)
	switch want {
	case kindTask:
		if isReminder && !isTask {
			return fmt.Errorf("'%s' is a reminder, not a task", name)
		}
	case kindReminder:
		if isTask && !isReminder {
			return fmt.Errorf("'%s' is a task, not a reminder", name)
		}
	}
	return nil
}
