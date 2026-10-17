package main

import (
	"github.com/serpro69/capy/internal/execution"
	"github.com/spf13/cobra"
)

func newExecuteSupervisorCmd() *cobra.Command {
	return &cobra.Command{
		Use:          execution.SupervisorCommand,
		Hidden:       true,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Task 2 supplies the authenticated runstore opener. Until then this
			// entry validates private descriptors and fails before any spawn.
			return execution.ServeInherited(cmd.Context(), nil)
		},
	}
}
