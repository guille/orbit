<p align="center">
  <img src="Orbit-logo.png" width="128" alt="Orbit logo">
</p>

# Orbit

[![CI](https://github.com/guille/orbit/actions/workflows/ci.yaml/badge.svg)](https://github.com/guille/orbit/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/v/release/guille/orbit)](https://github.com/guille/orbit/releases)

A declarative CLI tool to manage tasks and reminders, using systemd as its back-end.

Define your scheduled tasks and reminders in a single TOML file, and Orbit manages the systemd timers and services for you.

## Who is this for?

Anyone on a systemd-based Linux system who wants an easier way to manage recurring tasks and reminders without hand-writing unit files.

## Quick start

```sh
# Build from source
go install go.guillerg.dev/orbit/cmd/orbit@latest
# or with mise
mise use -g github:guille/orbit
# or download a published release

# Create and edit your config
orbit edit

# Preview what will change
orbit plan

# Apply the configuration
orbit apply

# show help
orbit help
```

## Example config

See [the example config](configs/sample.toml).

Schedules use systemd's [OnCalendar syntax](https://www.freedesktop.org/software/systemd/man/latest/systemd.time.html#Calendar%20Events).

## Internals

This section goes into a bit more detail of what happens behind the scenes when Orbit is managing units.

orbit maintains its own state file (default location: `~/.local/share/orbit/state.json`) tracking last run time, last exit code, retry state, pending reminders, and the **applied configuration**. This means changes to `orbit.toml` do **not** take effect until `orbit apply` is run. This is intentional, it prevents a half-edited config from breaking running tasks.

There are two supported types of configuration: tasks and reminders.

### Tasks

A task is a command defined and ran by systemd on the given frequency, with the configured retries. Orbit manages the systemd entries and gathers some data (last run, exit code, etc.).

Instead of using systemd for the retries, orbit registers itself as the task runner with a hidden command (`orbit _run $taskname`). When called, it reads the command and retry settings from the applied config in the state store (not from `orbit.toml`), handles execution and retries, and updates the state store.

Each task becomes two files: a .timer and a .service unit.

- `orbit-task-NAME.service` — runs the command, has `Type=oneshot`
- `orbit-task-NAME.timer` — with `Persistent=true` for catch-up

`orbit _run` exits with a code that tells systemd apart *what* failed:

| exit | meaning |
|---|---|
| `0` | the command succeeded |
| `10` | the command failed after all retries; orbit did its job |
| `1` | orbit itself failed (unreadable state, task not applied, bad `retry.delay`) |

The command's own exit code never leaks into the process exit code; it is recorded as `last_exit_code` in the state file.

A run suppressed by `orbit skip` (see Reminders below) exits `0` without running the command and touches none of the run-tracking state: it is neither a run nor a failure, and never fires `if_failed`.

#### Failure hooks

A task may declare an `if_failed` hook:

```toml
[tasks.backup]
command           = "rsync -av ~/Documents /mnt/backup/"
schedule          = "*-*-* 03:00:00"
if_failed.command = "notify-send \"backup failed\" \"$ORBIT_TASK exited $ORBIT_EXIT_CODE\""
if_failed.after   = 2
```

The hook runs once per retry cycle (only after every attempt has failed), so a task with `retry.attempts = 3` produces one hook run rather than three. `after` optionally configures the number of consecutive failed cycles (default 1) to wait for until the hook triggers. State is written before the hook runs, and a failing hook only prints a warning: it can neither lose the failure record nor change the exit code.

The hook inherits orbit's environment plus:

| variable | value |
|---|---|
| `ORBIT_TASK` | task name |
| `ORBIT_EXIT_CODE` | the command's exit code on the last attempt |
| `ORBIT_ATTEMPTS` | attempts made in this cycle |
| `ORBIT_CONSECUTIVE_FAILURES` | failed attempts since the last success |
| `ORBIT_FAILED_CYCLES` | failed cycles since the last success |
| `ORBIT_DURATION_MS` | duration of the last attempt |


### Reminders

Reminders get a service that calls `orbit _notify NAME`, **not the user's command**. The user-configured commands are never executed autonomously, it is the user that must run them.

Reminders stack. So if there is a daily reminder that hasn't been acknowledged for a week, the status might show as "7 overdue".

When a reminder fires, a desktop notification is sent via `notify-send`.

The command `orbit ack $reminder_name` acknowledges the reminder: marks it done, clears the pending state, and prompts to run the command (if set).

The user may also wish to snooze a reminder. This can be done with `orbit snooze`. Internally, orbit writes "snoozed until T" to state, and then creates a systemd timer to trigger the reminder again.

A reminder (or task) can also be skipped: `orbit skip NAME` suppresses the next scheduled firing, `--next 3` the next three, `--until Monday` everything before Monday 00:00. Unlike `disable`, the timer stays armed and nothing needs re-enabling. Unlike `snooze`, nothing is pending: a pending reminder must be acked or snoozed before it can be skipped. Internally, orbit writes "skip until T" to state and the service consults it when the timer fires, so a `Persistent=` catch-up for a skipped occurrence is swallowed too, and a journal line records why nothing happened. `orbit unskip` clears it, and so does `orbit disable`.

A simple sentinel fine (default location ~/.local/share/orbit/pending) is created when there is at least one reminder that needs to be acknowledged. It contains the number of pending reminders as a number. This can be used for scripting or shell integration.


## Shell completions

Orbit supports completions for Bash, Zsh, Fish, and PowerShell via Cobra's built-in completion command:

```sh
# Bash
orbit completion bash > ~/.local/share/bash-completion/completions/orbit

# Zsh
orbit completion zsh > "${fpath[1]}/_orbit"

# Fish
orbit completion fish > ~/.config/fish/completions/orbit.fish
```

Restart your shell or source the file to activate.
