package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"go.guillerg.dev/orbit/internal/config"
	"go.guillerg.dev/orbit/internal/state"
)

func planCmd() *cobra.Command {
	return withConfigFlag(&cobra.Command{
		Use:     "plan",
		Aliases: []string{"p"},
		Short:   "Preview changes without applying",
		Long:    `Show what orbit apply would do without making any changes. Compares the current configuration against the last applied configuration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}

			stateStore, err := newState()
			if err != nil {
				return err
			}

			_, err = showPlan(cfg, stateStore)
			return err
		},
	})
}

// showPlan compares the config against applied state and prints the change summary.
func showPlan(cfg *config.Config, stateStore *state.State) (configChangeSet, error) {
	cs := diffConfig(cfg, stateStore.GetAppliedConfig())

	if len(cs.changes) == 0 {
		fmt.Println("No changes needed. Configuration is up to date.")
		return cs, nil
	}

	printConfigChanges(cs)

	fmt.Printf("\nPlan: %d to create, %d to update, %d to remove, %d unchanged.\n",
		cs.nCreate, cs.nUpdate, cs.nRemove, cs.nUnchanged)
	fmt.Println(dim("Run 'orbit apply' to apply these changes."))
	return cs, nil
}
