package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"go.guillerg.dev/orbit/internal/config"
)

// State represents the orbit state store.
type State struct {
	filePath string
	mu       sync.RWMutex
	data     stateData

	// dirtyTasks and dirtyReminders track which keys were modified by this
	// process via SetTaskState/SetReminderState/Delete*. Only these keys
	// are overlaid onto disk state during reloadUnderLock, preventing stale
	// in-memory copies from overwriting changes made by other processes.
	dirtyTasks     map[string]bool
	dirtyReminders map[string]bool
	// deletedTasks and deletedReminders track keys explicitly deleted by
	// this process, so they are removed from disk state during merge.
	deletedTasks       map[string]bool
	deletedReminders   map[string]bool
	dirtyAppliedConfig bool
	dirtyEmbedVersion  bool
}

// stateData contains all tracked state information.
type stateData struct {
	Tasks         map[string]TaskState     `json:"tasks"`
	Reminders     map[string]ReminderState `json:"reminders"`
	Pending       int                      `json:"pending"`
	EmbedVersion  int                      `json:"embed_version,omitempty"`
	AppliedConfig *AppliedConfig           `json:"applied_config,omitempty"`
}

// AppliedConfig stores the config as it was when `orbit apply` was last run.
// This is the source of truth for _run, _notify, and all non-apply commands.
type AppliedConfig struct {
	OrbitBin  string                           `json:"orbit_bin"`
	Tasks     map[string]AppliedTaskConfig     `json:"tasks"`
	Reminders map[string]AppliedReminderConfig `json:"reminders"`
}

// HasTask reports whether name is registered as an applied task.
func (a *AppliedConfig) HasTask(name string) bool {
	if a == nil {
		return false
	}
	_, ok := a.Tasks[name]
	return ok
}

// HasReminder reports whether name is registered as an applied reminder.
func (a *AppliedConfig) HasReminder(name string) bool {
	if a == nil {
		return false
	}
	_, ok := a.Reminders[name]
	return ok
}

// Classify reports whether name is registered as a task, a reminder, or both.
func (a *AppliedConfig) Classify(name string) (isTask, isReminder bool) {
	return a.HasTask(name), a.HasReminder(name)
}

// IsEmpty reports whether no tasks or reminders have been applied.
func (a *AppliedConfig) IsEmpty() bool {
	return a == nil || (len(a.Tasks) == 0 && len(a.Reminders) == 0)
}

// Names returns all task and reminder names, unsorted.
func (a *AppliedConfig) Names() []string {
	if a == nil {
		return nil
	}
	names := make([]string, 0, len(a.Tasks)+len(a.Reminders))
	for name := range a.Tasks {
		names = append(names, name)
	}
	for name := range a.Reminders {
		names = append(names, name)
	}
	return names
}

// TaskNames returns all task names, unsorted.
func (a *AppliedConfig) TaskNames() []string {
	if a == nil {
		return nil
	}
	names := make([]string, 0, len(a.Tasks))
	for name := range a.Tasks {
		names = append(names, name)
	}
	return names
}

// ReminderNames returns all reminder names, unsorted.
func (a *AppliedConfig) ReminderNames() []string {
	if a == nil {
		return nil
	}
	names := make([]string, 0, len(a.Reminders))
	for name := range a.Reminders {
		names = append(names, name)
	}
	return names
}

// TaskSchedules returns the schedule of every task, unsorted and possibly
// with duplicates. Unscheduled (manual) tasks contribute an empty string.
func (a *AppliedConfig) TaskSchedules() []string {
	if a == nil {
		return nil
	}
	schedules := make([]string, 0, len(a.Tasks))
	for _, t := range a.Tasks {
		schedules = append(schedules, t.Schedule)
	}
	return schedules
}

// Schedules returns the schedule of every task and reminder, unsorted and
// possibly with duplicates.
func (a *AppliedConfig) Schedules() []string {
	if a == nil {
		return nil
	}
	schedules := a.TaskSchedules()
	for _, r := range a.Reminders {
		schedules = append(schedules, r.Schedule)
	}
	return schedules
}

// AppliedTaskConfig is the applied (saved) version of a task's configuration.
type AppliedTaskConfig struct {
	Command  string                `json:"command"`
	Schedule string                `json:"schedule"`
	OnMissed config.OnMissedPolicy `json:"on_missed"`
	Retry    AppliedRetryConfig    `json:"retry"`
	IfFailed AppliedHookConfig     `json:"if_failed,omitzero"`
}

// AppliedRetryConfig is the applied version of retry settings.
type AppliedRetryConfig struct {
	Attempts int    `json:"attempts"`
	Delay    string `json:"delay"`
}

// AppliedHookConfig is the applied version of a task hook.
type AppliedHookConfig struct {
	Command string `json:"command"`
	After   int    `json:"after"`
}

// AppliedReminderConfig is the applied (saved) version of a reminder's configuration.
type AppliedReminderConfig struct {
	Command  string `json:"command,omitempty"`
	Schedule string `json:"schedule"`
	Message  string `json:"message"`
	Snooze   string `json:"snooze,omitempty"`
	Check    string `json:"check,omitempty"`
}

// TaskState tracks the state of a task.
type TaskState struct {
	LastRun             time.Time `json:"last_run"`
	LastExitCode        int       `json:"last_exit_code"`
	LastDurationMs      int64     `json:"last_duration_ms"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	// FailedCycles counts consecutive retry cycles that ended in failure.
	// Unlike ConsecutiveFailures it ignores individual attempts. Reset on success.
	FailedCycles int  `json:"failed_cycles,omitempty"`
	RetryAttempt int  `json:"retry_attempt"`
	Disabled     bool `json:"disabled,omitempty"`
	// SkipUntil suppresses scheduled runs at or before this instant; the task
	// resumes with the first occurrence after it. See 'orbit skip'.
	SkipUntil *time.Time `json:"skip_until,omitempty"`
}

// ReminderStatus represents the current state of a reminder.
type ReminderStatus string

// Reminder state constants.
const (
	StatePending      ReminderStatus = "pending"
	StateAcknowledged ReminderStatus = "acknowledged"
	StateSnoozed      ReminderStatus = "snoozed"
)

func (rs ReminderStatus) String() string {
	if rs == "" {
		return "new"
	}
	return string(rs)
}

// ReminderState tracks the state of a reminder.
type ReminderState struct {
	State             ReminderStatus `json:"state"`
	FiredAt           time.Time      `json:"fired_at"`
	SnoozedUntil      *time.Time     `json:"snoozed_until,omitempty"`
	OverdueCount      int            `json:"overdue_count"`
	LastCheckExitCode *int           `json:"last_check_exit_code,omitempty"`
	LastCheckAt       time.Time      `json:"last_check_at"`
	Disabled          bool           `json:"disabled,omitempty"`
	// SkipUntil suppresses scheduled firings at or before this instant; the
	// reminder resumes with the first occurrence after it. See 'orbit skip'.
	SkipUntil *time.Time `json:"skip_until,omitempty"`
}

// NewState creates a new state instance, loading existing state from disk if available.
func NewState(dataDir string) (*State, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("creating state directory: %w", err)
	}

	s := &State{
		filePath: filepath.Join(dataDir, "state.json"),
		data: stateData{
			Tasks:     make(map[string]TaskState),
			Reminders: make(map[string]ReminderState),
		},
		dirtyTasks:       make(map[string]bool),
		dirtyReminders:   make(map[string]bool),
		deletedTasks:     make(map[string]bool),
		deletedReminders: make(map[string]bool),
	}

	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("loading state: %w", err)
	}

	return s, nil
}

// load reads the state from disk.
func (s *State) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			s.initDefaults()
			return nil
		}
		return fmt.Errorf("reading state file: %w", err)
	}

	if len(data) == 0 {
		s.initDefaults()
		return nil
	}

	if err := json.Unmarshal(data, &s.data); err != nil {
		return fmt.Errorf("parsing state JSON: %w", err)
	}

	if s.data.Tasks == nil {
		s.data.Tasks = make(map[string]TaskState)
	}
	if s.data.Reminders == nil {
		s.data.Reminders = make(map[string]ReminderState)
	}

	return nil
}

// initDefaults resets state data to empty, initialized maps.
func (s *State) initDefaults() {
	s.data = stateData{
		Tasks:     make(map[string]TaskState),
		Reminders: make(map[string]ReminderState),
	}
}

// Save writes the state to disk atomically and updates the sentinel file.
// It uses flock to prevent concurrent orbit processes from clobbering each other.
func (s *State) Save() error {
	return s.save(nil)
}

// UpdateTaskState applies fn to the task's state as it is on disk at the
// moment of writing, then saves. Long-running processes (the task runner)
// must write this way: a whole-entry Save would clobber fields another
// process changed meanwhile, such as a skip set while the task was running.
func (s *State) UpdateTaskState(name string, fn func(*TaskState)) error {
	return s.save(func() {
		ts := s.data.Tasks[name]
		fn(&ts)
		s.data.Tasks[name] = ts
		s.dirtyTasks[name] = true
	})
}

// UpdateReminderState is UpdateTaskState for reminders.
func (s *State) UpdateReminderState(name string, fn func(*ReminderState)) error {
	return s.save(func() {
		rs := s.data.Reminders[name]
		fn(&rs)
		s.data.Reminders[name] = rs
		s.dirtyReminders[name] = true
	})
}

// save merges in-memory changes over the on-disk state under the file lock,
// runs mutate (if any) against that fresh merge, and writes the result.
func (s *State) save(mutate func()) error {
	// Acquire an exclusive file lock to prevent concurrent processes from
	// interleaving read-modify-write cycles on the state file.
	lockFile, err := os.OpenFile(s.filePath+".lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	//nolint:errcheck
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring file lock: %w", err)
	}
	//nolint:errcheck
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	// Re-read state from disk under the lock to pick up any changes
	// made by other processes since we last loaded.
	// Hold s.mu across reload + marshal to prevent goroutine mutations
	// from slipping in between merge and serialization.
	s.mu.Lock()

	if err := s.reloadUnderLock(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("reloading state under lock: %w", err)
	}
	if mutate != nil {
		mutate()
	}

	// Compute pending count from the map for JSON serialization.
	pending := 0
	for _, rs := range s.data.Reminders {
		if rs.State == StatePending {
			pending++
		}
	}
	s.data.Pending = pending

	data, err := json.MarshalIndent(s.data, "", "  ")

	// Save dirty state so we can restore on write failure.
	oldDirtyTasks := s.dirtyTasks
	oldDirtyReminders := s.dirtyReminders
	oldDeletedTasks := s.deletedTasks
	oldDeletedReminders := s.deletedReminders
	oldDirtyAppliedConfig := s.dirtyAppliedConfig
	oldDirtyEmbedVersion := s.dirtyEmbedVersion

	// Clear dirty/deleted tracking now that changes are captured in data.
	s.dirtyTasks = make(map[string]bool)
	s.dirtyReminders = make(map[string]bool)
	s.deletedTasks = make(map[string]bool)
	s.deletedReminders = make(map[string]bool)
	s.dirtyAppliedConfig = false
	s.dirtyEmbedVersion = false

	s.mu.Unlock()

	if err != nil {
		s.restoreDirtyState(oldDirtyTasks, oldDirtyReminders, oldDeletedTasks, oldDeletedReminders, oldDirtyAppliedConfig, oldDirtyEmbedVersion)
		return fmt.Errorf("marshaling state: %w", err)
	}

	tmpFile := s.filePath + ".tmp"
	if err := writeFileSync(tmpFile, data); err != nil {
		s.restoreDirtyState(oldDirtyTasks, oldDirtyReminders, oldDeletedTasks, oldDeletedReminders, oldDirtyAppliedConfig, oldDirtyEmbedVersion)
		return fmt.Errorf("writing temporary state file: %w", err)
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		s.restoreDirtyState(oldDirtyTasks, oldDirtyReminders, oldDeletedTasks, oldDeletedReminders, oldDirtyAppliedConfig, oldDirtyEmbedVersion)
		return fmt.Errorf("renaming state file: %w", err)
	}
	syncDir(filepath.Dir(s.filePath))

	s.updateSentinelFile()
	return nil
}

// writeFileSync writes data to path and flushes it to stable storage. The
// rename that follows is atomic for other readers either way, but without the
// flush a power loss can land the rename while the contents are still missing,
// leaving an empty state file that load() reads as "nothing configured".
func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		//nolint:errcheck
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		//nolint:errcheck
		f.Close()
		return err
	}
	return f.Close()
}

// syncDir flushes a directory entry so that a rename into it survives a power
// loss. Best-effort: by the time it runs the rename has already succeeded and
// every reader sees the new state, so a failure here is not worth failing the
// save over.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	//nolint:errcheck
	d.Sync()
	//nolint:errcheck
	d.Close()
}

// restoreDirtyState re-marks fields as dirty after a failed save, ensuring the next Save() retries.
func (s *State) restoreDirtyState(tasks, reminders, delTasks, delReminders map[string]bool, appliedConfig, embedVersion bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range tasks {
		s.dirtyTasks[k] = true
	}
	for k := range reminders {
		s.dirtyReminders[k] = true
	}
	for k := range delTasks {
		s.deletedTasks[k] = true
	}
	for k := range delReminders {
		s.deletedReminders[k] = true
	}
	s.dirtyAppliedConfig = s.dirtyAppliedConfig || appliedConfig
	s.dirtyEmbedVersion = s.dirtyEmbedVersion || embedVersion
}

// reloadUnderLock re-reads the state file from disk and merges it with
// in-memory state. This must only be called while holding both the file lock
// and s.mu. It merges by taking disk state as baseline and overlaying any
// in-memory changes for tasks/reminders that this process has modified.
//
// NOTE: If two processes modify the same key simultaneously (e.g., a snooze
// timer and a regular timer fire for the same reminder at the same instant),
// the second writer's overlay will overwrite the first. This is acceptable
// because it requires near-simultaneous timer fires for the same entry, which
// is extremely unlikely in practice.
func (s *State) reloadUnderLock() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no file on disk yet, our in-memory state is canonical
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}

	var diskData stateData
	if err := json.Unmarshal(data, &diskData); err != nil {
		return err
	}

	// Merge: disk state is the base. Only overlay keys that this process
	// has explicitly modified (dirty), and remove keys that were deleted.
	if diskData.Tasks == nil {
		diskData.Tasks = make(map[string]TaskState)
	}
	if diskData.Reminders == nil {
		diskData.Reminders = make(map[string]ReminderState)
	}

	// Apply dirty task/reminder writes
	for name := range s.dirtyTasks {
		if ts, ok := s.data.Tasks[name]; ok {
			diskData.Tasks[name] = ts
		}
	}
	for name := range s.dirtyReminders {
		if rs, ok := s.data.Reminders[name]; ok {
			diskData.Reminders[name] = rs
		}
	}

	// Apply deletions
	for name := range s.deletedTasks {
		delete(diskData.Tasks, name)
	}
	for name := range s.deletedReminders {
		delete(diskData.Reminders, name)
	}

	// Apply dirty applied config
	if s.dirtyAppliedConfig {
		diskData.AppliedConfig = s.data.AppliedConfig
	}

	// Apply dirty embed version
	if s.dirtyEmbedVersion {
		diskData.EmbedVersion = s.data.EmbedVersion
	}

	// Recalculate pending count from merged data
	pending := 0
	for _, rs := range diskData.Reminders {
		if rs.State == StatePending {
			pending++
		}
	}
	diskData.Pending = pending

	s.data = diskData
	return nil
}

// updateSentinelFile writes or removes the pending sentinel file.
func (s *State) updateSentinelFile() {
	s.mu.RLock()
	pending := s.data.Pending
	s.mu.RUnlock()

	sentinelFile := filepath.Join(filepath.Dir(s.filePath), "pending")

	if pending > 0 {
		if err := os.WriteFile(sentinelFile, fmt.Appendf(nil, "%d", pending), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "[ORBIT] Warning: failed to update sentinel file: %v\n", err)
		}
	} else {
		if err := os.Remove(sentinelFile); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "[ORBIT] Warning: failed to remove sentinel file: %v\n", err)
		}
	}
}

// GetTaskState returns the state for a task.
func (s *State) GetTaskState(name string) TaskState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Tasks[name]
}

// SetTaskState updates the state for a task.
func (s *State) SetTaskState(name string, ts TaskState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Tasks[name] = ts
	s.dirtyTasks[name] = true
	delete(s.deletedTasks, name)
}

// GetReminderState returns the state for a reminder.
func (s *State) GetReminderState(name string) ReminderState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Reminders[name]
}

// SetReminderState updates the state for a reminder.
func (s *State) SetReminderState(name string, rs ReminderState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Reminders[name] = rs
	s.dirtyReminders[name] = true
	delete(s.deletedReminders, name)
}

// DeleteTaskState removes a task's state entry.
func (s *State) DeleteTaskState(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Tasks, name)
	s.deletedTasks[name] = true
	delete(s.dirtyTasks, name)
}

// DeleteReminderState removes a reminder's state entry.
func (s *State) DeleteReminderState(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Reminders, name)
	s.deletedReminders[name] = true
	delete(s.dirtyReminders, name)
}

// Pending returns the current count of pending reminders, computed from the map.
func (s *State) Pending() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, rs := range s.data.Reminders {
		if rs.State == StatePending {
			count++
		}
	}
	return count
}

// GetAppliedConfig returns the applied configuration, or nil if none has been applied yet.
func (s *State) GetAppliedConfig() *AppliedConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.AppliedConfig
}

// SetAppliedConfig stores the applied configuration. This is called by `orbit apply`.
func (s *State) SetAppliedConfig(ac *AppliedConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.AppliedConfig = ac
	s.dirtyAppliedConfig = true
}

// GetAppliedTask returns the applied config for a task, and whether it exists.
func (s *State) GetAppliedTask(name string) (AppliedTaskConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data.AppliedConfig == nil {
		return AppliedTaskConfig{}, false
	}
	t, ok := s.data.AppliedConfig.Tasks[name]
	return t, ok
}

// GetAppliedReminder returns the applied config for a reminder, and whether it exists.
func (s *State) GetAppliedReminder(name string) (AppliedReminderConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data.AppliedConfig == nil {
		return AppliedReminderConfig{}, false
	}
	r, ok := s.data.AppliedConfig.Reminders[name]
	return r, ok
}

// GetEmbedVersion returns the stored embed version.
func (s *State) GetEmbedVersion() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.EmbedVersion
}

// SetEmbedVersion updates the stored embed version.
func (s *State) SetEmbedVersion(v int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.EmbedVersion = v
	s.dirtyEmbedVersion = true
}
