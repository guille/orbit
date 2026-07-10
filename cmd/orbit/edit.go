package main

import (
	"cmp"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/config"
)

//go:embed schema.json
var schemaJSON []byte

func editCmd() *cobra.Command {
	var detach bool

	cmd := withConfigFlag(&cobra.Command{
		Use:     "edit",
		Aliases: []string{"e", "ed"},
		Short:   "Open config in $EDITOR",
		Long:    `Open the orbit configuration file in the editor specified by $EDITOR.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			editorStr := cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")

			configPath, err := getConfigPathFromCmd(cmd)
			if err != nil {
				return err
			}
			configDir := filepath.Dir(configPath)

			if err := os.MkdirAll(configDir, 0755); err != nil {
				return fmt.Errorf("creating config directory: %w", err)
			}

			stateStore, err := newState()
			if err != nil {
				return err
			}
			ensureEmbeddedAssets(stateStore)

			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				stub := []byte(`#:schema ./schema.json
# Orbit configuration
# Schedule values use systemd OnCalendar syntax.
# See: https://www.freedesktop.org/software/systemd/man/latest/systemd.time.html#Calendar%20Events
# Test with: systemd-analyze calendar "daily"

# include = ["orbit.d/*.toml", "?local.toml"]  # merge other TOML files ('*' globs, leading '?' = optional)

# [tasks.example]
# command        = "echo hello"
# schedule       = "*-*-* 03:00:00"  # omit for manual-only tasks
# on_missed      = "run_once"        # run_once | skip (default: run_once)
# retry.attempts = 3
# retry.delay    = "5m"

# [reminders.example]
# schedule = "Mon *-*-* 09:00:00"
# message  = "Time for your weekly review"
# command  = "echo review"           # optional: offered on ack
# snooze   = "2h"                    # default snooze duration
# check    = "test -f /tmp/flag"     # only fires if this exits 0
`)
				if err := os.WriteFile(configPath, stub, 0644); err != nil {
					return fmt.Errorf("creating config file: %w", err)
				}
			}

			parts := strings.Fields(editorStr)
			editorCmd := parts[0]
			editorArgs := append(append([]string{}, parts[1:]...), configPath)

			c := exec.Command(editorCmd, editorArgs...)
			c.Stdin = os.Stdin
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr

			if detach {
				if err := c.Start(); err != nil {
					return fmt.Errorf("opening editor: %w", err)
				}
				fmt.Printf("Opened %s in %s\n", configPath, editorCmd)
				return nil
			}

			fmt.Printf("%s", dim("Waiting for the editor to close..."))
			if err := c.Run(); err != nil {
				fmt.Printf("\r\033[K") // clear the waiting message
				return fmt.Errorf("opening editor: %w", err)
			}
			fmt.Printf("\r\033[K") // clear the waiting message

			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s config has errors: %v\n", yellow("Warning:"), err)
				fmt.Fprintln(os.Stderr, "Fix the errors before running 'orbit apply'.")
				return nil
			}
			if err := cfg.Validate(); err != nil {
				fmt.Fprintf(os.Stderr, "%s config validation failed: %v\n", yellow("Warning:"), err)
				fmt.Fprintln(os.Stderr, "Fix the errors before running 'orbit apply'.")
				return nil
			}

			fmt.Println("Config is valid.")
			fmt.Println()

			stateStore, err = newState()
			if err != nil {
				return err
			}

			cs, err := showPlan(cfg, stateStore)
			if err != nil {
				return err
			}

			if len(cs.changes) == 0 {
				return nil
			}

			fmt.Println()
			if !confirm("Apply these changes?") {
				fmt.Printf("Run %s when you're ready.\n", bold("orbit apply"))
				return nil
			}

			fmt.Println()
			return runApply(cfg, stateStore, &cs, false)
		},
	})

	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "Open editor and return immediately without waiting")
	return cmd
}
