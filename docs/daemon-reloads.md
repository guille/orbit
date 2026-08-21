# When to reload the systemd manager

Orbit writes unit files and then asks systemd to act on them. Between those two
steps sits a question that is easy to get wrong in both directions: must we run
`systemctl daemon-reload`? A needless reload is expensive — it rebuilds the
manager's entire dependency graph, and a quarter of a second is a long time in a
command a person is waiting on. A missing one leaves systemd acting on a unit
file we have already replaced. This note records the rule so that neither
mistake need be rediscovered.

## What a reload actually does

The manager does not consult unit files as it runs; it reads them once and keeps
the result. A reload discards that memory and reads afresh. The manual is
explicit that this is a whole-system operation, not a per-unit one:

> Reload the systemd manager configuration. This will rerun all generators,
> reload all unit files, and recreate the entire dependency tree.
> — `systemctl(1)`, *daemon-reload*

Hence the cost, and hence the first thing to understand: what is in that memory
at all.

## Loaded and unloaded units

A unit occupies the manager's memory only while something needs it.
`systemd.unit(5)`, under *Unit Garbage Collection*, says the manager

> loads a unit's configuration automatically when a unit is referenced for the
> first time. It will automatically unload the unit configuration and state
> again when the unit is not needed anymore.

and lists what counts as needing it: another loaded unit depends on it; it is
starting, running, reloading or stopping; it is in the failed state; a job for
it is pending; an IPC client has pinned it; it has running processes.

This distinction decides everything. A unit that is *not* loaded has no stale
copy to speak of, and will be read from disk the next time it is referenced. A
unit that *is* loaded — an active timer, say — will go on using the
configuration it was given, however often we rewrite the file beneath it. The
manager tracks this itself, in a property worth knowing:

> `NeedDaemonReload` is a boolean that indicates whether the configuration file
> this unit is loaded from has changed since the configuration was read.
> — `org.freedesktop.systemd1(5)`

Editing a unit file is therefore hazardous only when the unit happens to be
loaded at that moment — a condition that turns on what else refers to it, and
which is not always evident from the outside. An enabled timer is certainly
loaded, being active in the interval between its firings.

## Enable and disable reload by themselves

The second half of the rule is that we frequently get a reload without asking
for one. Of `enable`, the manual says that after the symlinks are created,

> the system manager configuration is reloaded (in a way equivalent to
> daemon-reload), in order to ensure the changes are taken into account
> immediately.

and of `disable`, that it

> implicitly reloads the system manager configuration after completing the
> operation.

Both refer to a full reload, so either will pick up unrelated edits made
beforehand. The behaviour can be suppressed with `--no-reload`, which we have no
present use for; it is mentioned only so that its absence is understood to be
deliberate.

A distinction from the same page is worth carrying alongside all this.
`list-units` shows "units that systemd currently has in memory", whereas
`list-unit-files` shows "unit files installed on the system". They answer
different questions, and a unit whose file we have deleted may still be reported
by the former after it has left the latter. Orbit's drift detection reads
`list-unit-files`, and so observes the filesystem rather than the manager's
recollection of it.

## The rule

Reload exactly when the operation will not otherwise cause one, and when
something loaded would be left holding a stale copy.

In practice this collapses to a single question at each site: *does an `enable`
or a `disable` run here?* If one does, it has reloaded on our behalf and an
explicit call merely repeats the work. If none does, we must reload ourselves,
because a unit that happened to be loaded when we rewrote its file would
otherwise keep the old text indefinitely.

Removal deserves one remark. Stopping a unit and deleting its file does not
require a reload for correctness: the unit is inactive, so it cannot fire, and
it has left `list-unit-files` already. The manual promises only that an
unneeded unit is unloaded eventually, not that it is unloaded at once, so
without a reload the dead unit may be reported by `list-units` for some while
yet. Since removal disables the timer anyway, and `disable` reloads, this costs
us nothing — provided we do not remove units without disabling any.

## Where orbit reloads

Two places, both in `internal/systemd/install.go`.

`InstallUnits` moves the staged files into place and then enables the timers
among them. The `enable` reloads, which covers both newly created units and
edits to existing ones. An explicit reload is needed only when the set contains
no timers at all — a configuration of purely manual tasks, which generates
services and enables nothing.

`RemoveUnits` stops the units, disables those that are timers, and unlinks the
files. The `disable` reloads, which sweeps the stopped units from memory. An
explicit reload is needed only when the set contains no timers.

Both must therefore guard the call on the same condition, and the guard is the
whole point: without it each function reloads twice, once of its own accord and
once inside `systemctl`.
