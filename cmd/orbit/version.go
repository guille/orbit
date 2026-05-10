package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version string = "dev"

func versionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "version",
		Short:   "Print the version information",
		Aliases: []string{"v"},
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Orbit version: %s\n", version)
			fmt.Printf("Embed version: %d\n", currentEmbedVersion)
			return nil
		},
	}

	return cmd
}
