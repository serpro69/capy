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
			return execution.ServeInherited(cmd.Context(), execution.OpenStoredRun)
		},
	}
}
