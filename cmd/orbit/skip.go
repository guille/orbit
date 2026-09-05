package main

import (
	"fmt"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/reminder"
	"go.guillerg.dev/orbit/internal/state"
	"go.guillerg.dev/orbit/internal/systemd"
)

// kindAny matches both tasks and reminders where a command accepts either.
const kindAny entryKind = ""

// skipCountCap bounds how many suppressed firings a skip reports. Beyond it
// the exact number is not worth another systemd-analyze round trip.
const skipCountCap = 100

// skipEntry is a task or reminder as 'skip' and 'unskip' see it.
type skipEntry struct {
	kind      entryKind
	name      string
	schedule  string
	disabled  bool
	pending   bool // reminders only: awaiting ack or snooze
	skipUntil *time.Time
}

// unskippable explains why the entry cannot be skipped, or returns nil.
func (e skipEntry) unskippable() error {
	switch {
	case e.disabled:
		return fmt.Errorf("'%s' is disabled and will not fire anyway; 'orbit enable %s' first", e.name, e.name)
	case e.schedule == "":
		return fmt.Errorf("'%s' has no schedule; nothing to skip", e.name)
	case e.pending:
		return fmt.Errorf("'%s' is pending; ack or snooze it, then skip", e.name)
	}
	return nil
}

// skipped reports whether the entry is under an active skip window.
func (e skipEntry) skipped() bool {
	return isSkipped(e.schedule, e.skipUntil)
}

// skipEntries returns every applied entry of the given kind (kindAny for both)
// carrying the name, or all of them when name is empty.
func skipEntries(stateStore *state.State, kind entryKind, name string) []skipEntry {
	applied := stateStore.GetAppliedConfig()
	if applied == nil {
		return nil
	}
	var entries []skipEntry
	if kind != kindReminder {
		for n, cfg := range applied.Tasks {
			if name != "" && n != name {
				continue
			}
			ts := stateStore.GetTaskState(n)
			entries = append(entries, skipEntry{kindTask, n, cfg.Schedule, ts.Disabled, false, ts.SkipUntil})
		}
	}
	if kind != kindTask {
		for n, cfg := range applied.Reminders {
			if name != "" && n != name {
				continue
			}
			rs := stateStore.GetReminderState(n)
			entries = append(entries, skipEntry{kindReminder, n, cfg.Schedule, rs.Disabled, reminder.IsActionable(rs), rs.SkipUntil})
		}
	}
	return entries
}

// setSkipUntil writes an entry's skip window to state without saving.
func setSkipUntil(stateStore *state.State, kind entryKind, name string, until *time.Time) {
	switch kind {
	case kindTask:
		ts := stateStore.GetTaskState(name)
		ts.SkipUntil = until
		stateStore.SetTaskState(name, ts)
	case kindReminder:
		rs := stateStore.GetReminderState(name)
		rs.SkipUntil = until
		stateStore.SetReminderState(name, rs)
	}
}

// clearSkip drops an entry's skip window and saves.
func clearSkip(stateStore *state.State, kind entryKind, name string) error {
	setSkipUntil(stateStore, kind, name, nil)
	if err := stateStore.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}
	return nil
}

// skipGate decides a scheduled start under a skip window: true means the
// start must be suppressed, and the decision is logged for the journal. A
// window that has run its course is cleared here, on the way through.
func skipGate(stateStore *state.State, kind entryKind, name, schedule string, skipUntil *time.Time) (bool, error) {
	if skipUntil == nil {
		return false, nil
	}
	resume, active := skipResume(schedule, skipUntil)
	if !active {
		return false, clearSkip(stateStore, kind, name)
	}
	if resume.IsZero() {
		fmt.Printf("[ORBIT] '%s' skipped; could not resolve its schedule to tell when it resumes\n", name)
	} else {
		fmt.Printf("[ORBIT] '%s' skipped; resumes %s\n", name, absTime(resume))
	}
	return true, nil
}

// pickSkipEntries resolves the entries a skip/unskip acts on: those named by
// args, or the user's pick among candidates that pass keep. A name shared by a
// task and a reminder yields both, as enable/disable do.
func pickSkipEntries(stateStore *state.State, args []string, kind entryKind, keep func(skipEntry) bool, prompt, none string) ([]skipEntry, error) {
	applied := stateStore.GetAppliedConfig()
	if applied.IsEmpty() {
		return nil, fmt.Errorf("nothing configured (run 'orbit apply' first)")
	}

	if len(args) > 0 {
		if err := rejectWrongKind(stateStore, args, kind); err != nil {
			return nil, err
		}
		entries := skipEntries(stateStore, kind, args[0])
		if len(entries) == 0 {
			return nil, notAppliedErr(kindLabel(kind), args[0])
		}
		return entries, nil
	}

	var names []string
	for _, e := range skipEntries(stateStore, kind, "") {
		if keep(e) && !slices.Contains(names, e.name) {
			names = append(names, e.name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s", none)
	}
	sortNatural(names)
	name, err := pickFromList(names, prompt)
	if err != nil {
		return nil, err
	}
	var picked []skipEntry
	for _, e := range skipEntries(stateStore, kind, name) {
		if keep(e) {
			picked = append(picked, e)
		}
	}
	return picked, nil
}

// kindLabel names a kind in messages; kindAny reads as "entry".
func kindLabel(kind entryKind) entryKind {
	if kind == kindAny {
		return "entry"
	}
	return kind
}

// completeSkipNames completes NAME for skip (entries that could be skipped) or
// unskip (entries carrying a skip). It stays cheap: no schedule resolution.
func completeSkipNames(kind entryKind, skipped bool) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		stateStore, err := newState()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var names []string
		for _, e := range skipEntries(stateStore, kind, "") {
			if (e.skipUntil != nil) == skipped && (skipped || e.unskippable() == nil) && !slices.Contains(names, e.name) {
				names = append(names, e.name)
			}
		}
		sortNatural(names)
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// skipPlan is a resolved skip window for one schedule.
type skipPlan struct {
	until  time.Time // last suppressed instant; stored as SkipUntil
	resume time.Time // first firing after the window
	count  int       // suppressed firings, saturating at skipCountCap+1
}

// planSkipNext resolves a skip of the next n occurrences. Suppressing every
// remaining firing of a terminating schedule is refused: that is 'disable'
// under another name, and 'apply' can later extend the schedule.
func planSkipNext(m *systemd.Manager, e skipEntry, n int) (skipPlan, error) {
	occ, err := m.Occurrences(e.schedule, n+1)
	switch {
	case err != nil:
		return skipPlan{}, fmt.Errorf("resolving schedule of '%s': %w", e.name, err)
	case len(occ) == 0:
		return skipPlan{}, fmt.Errorf("'%s' has nothing scheduled", e.name)
	case len(occ) == 1:
		return skipPlan{}, fmt.Errorf("'%s' fires only once more (%s); skipping it would mean it never fires, use 'orbit disable %s' instead", e.name, absTime(occ[0]), e.name)
	case len(occ) <= n:
		return skipPlan{}, fmt.Errorf("'%s' has %d firings left before its schedule ends; skip at most %d, or 'orbit disable %s'", e.name, len(occ), len(occ)-1, e.name)
	}
	return skipPlan{until: occ[n-1], resume: occ[n], count: n}, nil
}

// planSkipUntil resolves a skip of every occurrence before `when`. A firing
// exactly at `when` still happens, so "skip until Monday 09:00" keeps Monday's
// 09:00 firing. The stored instant sits one microsecond before `when`, the
// finest step --base-time resolves, so that boundary holds for sub-second
// inputs like "--until 24h" too.
func planSkipUntil(m *systemd.Manager, e skipEntry, when time.Time) (skipPlan, error) {
	until := when.Add(-time.Microsecond)
	occ, err := m.Occurrences(e.schedule, skipCountCap+1)
	count := 0
	for count < len(occ) && occ[count].Before(when) {
		count++
	}
	switch {
	case err != nil:
		return skipPlan{}, fmt.Errorf("resolving schedule of '%s': %w", e.name, err)
	case len(occ) == 0:
		return skipPlan{}, fmt.Errorf("'%s' has nothing scheduled", e.name)
	case count == 0:
		return skipPlan{}, fmt.Errorf("'%s' has no firings before %s (next is %s); nothing to skip", e.name, absTime(when), absTime(occ[0]))
	}

	plan := skipPlan{until: until, count: count}
	if count < len(occ) {
		plan.resume = occ[count]
		return plan, nil
	}
	resume, ok, err := m.NextAfter(e.schedule, until)
	if err != nil {
		return skipPlan{}, fmt.Errorf("resolving schedule of '%s': %w", e.name, err)
	}
	if !ok {
		return skipPlan{}, fmt.Errorf("'%s' has nothing scheduled after %s; use 'orbit disable %s' instead", e.name, absTime(when), e.name)
	}
	plan.resume = resume
	return plan, nil
}

// skipCommand is the shared implementation behind every 'skip' mount point.
func skipCommand(args []string, kind entryKind, next int, until string) error {
	if next < 1 {
		return fmt.Errorf("--next must be at least 1")
	}

	stateStore, err := newState()
	if err != nil {
		return err
	}

	skippable := func(e skipEntry) bool { return e.unskippable() == nil && !e.skipped() }
	entries, err := pickSkipEntries(stateStore, args, kind, skippable, "Select entry to skip:",
		fmt.Sprintf("no %s can be skipped (need a schedule, enabled, not pending and not already skipped)", kindPlural(kind)))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := e.unskippable(); err != nil {
			return err
		}
	}

	manager := systemd.NewManager()
	var when time.Time
	if until != "" {
		if when, err = manager.ResolveTime(until); err != nil {
			return err
		}
		if !when.After(time.Now()) {
			return fmt.Errorf("%q resolves to %s, which is in the past", until, absTime(when))
		}
	}

	// Resolve every window before touching state, so a refusal leaves nothing
	// half-applied.
	plans := make([]skipPlan, len(entries))
	for i, e := range entries {
		if until != "" {
			plans[i], err = planSkipUntil(manager, e, when)
		} else {
			plans[i], err = planSkipNext(manager, e, next)
		}
		if err != nil {
			return err
		}
	}

	for i, e := range entries {
		setSkipUntil(stateStore, e.kind, e.name, &plans[i].until)
	}
	if err := stateStore.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	for i, e := range entries {
		fmt.Printf("%s '%s' %s (%s)\n", titleKind(e.kind), bold(e.name), yellow("skipped"), skipSummary(e, plans[i]))
	}
	return nil
}

// skipSummary describes a freshly applied skip: how much it suppresses and
// when the entry resumes.
func skipSummary(e skipEntry, plan skipPlan) string {
	s := "resumes " + formatTime(plan.resume)
	if plan.count > 1 {
		n := fmt.Sprintf("%d", plan.count)
		if plan.count > skipCountCap {
			n = fmt.Sprintf("%d+", skipCountCap)
		}
		s = n + " firings, " + s
	}
	if e.skipped() {
		s += ", replaces previous skip"
	}
	return s
}

// unskipCommand is the shared implementation behind every 'unskip' mount point.
func unskipCommand(args []string, kind entryKind) error {
	stateStore, err := newState()
	if err != nil {
		return err
	}

	entries, err := pickSkipEntries(stateStore, args, kind, skipEntry.skipped, "Select entry to unskip:",
		fmt.Sprintf("no %s are skipped", kindPlural(kind)))
	if err != nil {
		return err
	}

	var cleared []skipEntry
	for _, e := range entries {
		if e.skipUntil == nil {
			continue
		}
		// An expired window is cleared too, silently: it already reads as absent.
		if e.skipped() {
			cleared = append(cleared, e)
		}
		setSkipUntil(stateStore, e.kind, e.name, nil)
	}
	if err := stateStore.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	if len(cleared) == 0 {
		fmt.Printf("'%s' is not skipped.\n", bold(entries[0].name))
		return nil
	}
	for _, e := range cleared {
		fmt.Printf("%s '%s' skip %s (next %s)\n", titleKind(e.kind), bold(e.name), green("cleared"), resolveNextRun(e.schedule))
	}
	return nil
}

// absTime formats an instant for messages where a relative time would not
// tell the user which firing is meant.
func absTime(t time.Time) string {
	return t.Format("Mon 2006-01-02 15:04")
}

// titleKind capitalizes a kind for the start of a sentence.
func titleKind(kind entryKind) string {
	switch kind {
	case kindTask:
		return "Task"
	case kindReminder:
		return "Reminder"
	}
	return "Entry"
}

// kindPlural is the plural noun for a kind: "tasks", "reminders" or "entries".
func kindPlural(kind entryKind) string {
	if kind == kindAny {
		return "entries"
	}
	return string(kind) + "s"
}

// newSkipCmd builds the skip command for one mount point. kind restricts the
// entries it accepts; kindAny resolves the kind from NAME.
func newSkipCmd(kind entryKind, short string) *cobra.Command {
	var next int
	var until string

	long := `Suppress upcoming scheduled firings without changing the schedule or
disabling the entry. With no flags, the next firing is skipped. The timer stays
armed; the skipped occurrence is swallowed when it fires, including a
Persistent= catch-up after the machine was off.

  --next N      skip the next N firings
  --until WHEN  skip every firing before WHEN; a firing exactly at WHEN still
                happens. WHEN is a duration (2h), a systemd timestamp
                (tomorrow, 2026-09-07 17:00) or a calendar expression
                (Monday, Fri 17:00). Bare days resolve to 00:00, so
                --until Monday keeps a Monday-morning firing.

Skipping a pending reminder is refused: ack or snooze it first. 'orbit run' on
a skipped task offers to clear the skip. Undo with 'orbit unskip'.`

	cmd := &cobra.Command{
		Use:               "skip [NAME]",
		Short:             short,
		Long:              long,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeSkipNames(kind, false),
		RunE: func(_ *cobra.Command, args []string) error {
			return skipCommand(args, kind, next, until)
		},
	}

	cmd.Flags().IntVarP(&next, "next", "n", 1, "Skip the next N firings")
	cmd.Flags().StringVarP(&until, "until", "u", "", "Skip every firing before WHEN (e.g. 2h, tomorrow, Monday, '2026-09-07 17:00')")
	cmd.MarkFlagsMutuallyExclusive("next", "until")

	return cmd
}

// newUnskipCmd builds the unskip command for one mount point.
func newUnskipCmd(kind entryKind, short string) *cobra.Command {
	return &cobra.Command{
		Use:               "unskip [NAME]",
		Short:             short,
		Long:              `Clear an active skip so the entry fires on its next scheduled occurrence.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeSkipNames(kind, true),
		RunE: func(_ *cobra.Command, args []string) error {
			return unskipCommand(args, kind)
		},
	}
}

func rootSkipCmd() *cobra.Command {
	return newSkipCmd(kindAny, "Skip the next scheduled firing of a task or reminder")
}

func rootUnskipCmd() *cobra.Command {
	return newUnskipCmd(kindAny, "Clear a skip on a task or reminder")
}

func taskSkipCmd() *cobra.Command   { return newSkipCmd(kindTask, "Skip the next scheduled run") }
func taskUnskipCmd() *cobra.Command { return newUnskipCmd(kindTask, "Clear a skip") }
func reminderSkipCmd() *cobra.Command {
	return newSkipCmd(kindReminder, "Skip the next scheduled firing")
}
func reminderUnskipCmd() *cobra.Command {
	return newUnskipCmd(kindReminder, "Clear a skip")
}
