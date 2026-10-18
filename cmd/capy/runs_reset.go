package main

import (
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/spf13/cobra"
)

func newRunsResetCmd() *cobra.Command {
	var discard bool
	cmd := &cobra.Command{
		Use: "reset --discard", Short: "Discard this owner's run logs without the old key",
		Long:          "Permanently discard this owner's captured run logs and SQLite sidecars. Stop all run owners/readers/supervisors first. If cleanup was unverified, also stop old execution groups. Permanent locks, knowledge and vault data are preserved. Never use reset as an automatic execution retry.",
		SilenceErrors: true, SilenceUsage: true,
	}
	invalid := func(cmd *cobra.Command) error {
		return writeQueryFailure(cmd, false, 2048, 2, &runstore.Error{Code: "invalid_argument", Message: "reset requires --discard and accepts no run selectors or positional arguments"})
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error { return invalid(cmd) })
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 0 {
			return invalid(cmd)
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if !discard {
			return invalid(cmd)
		}
		target, err := runTarget(cmd)
		if err != nil {
			return writeQueryFailure(cmd, false, 2048, 1, err)
		}
		path, err := config.ResolveRunDBPath(target.projectDir)
		if err == nil {
			err = runstore.Reset(cmd.Context(), path)
		}
		if err != nil {
			return writeQueryFailure(cmd, false, target.cfg.Runs.MaxResponseBytes, 1, err)
		}
		_, err = cmd.OutOrStdout().Write([]byte("Run logs discarded. The next execution creates a new encrypted run store with the selected credential.\n"))
		return err
	}
	cmd.Flags().BoolVar(&discard, "discard", false, "confirm permanent loss of this owner's run logs")
	return cmd
}
