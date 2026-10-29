package main

import (
	"errors"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/spf13/cobra"
)

func newRunsUpgradeCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{Use: "upgrade", Short: "Upgrade offline without executing commands or discarding history", SilenceErrors: true, SilenceUsage: true}
	cmd.Long = "Stop all run owners, readers and supervisors, then run this command from an ordinary terminal. Authenticates and upgrades schema 8/9 to 10 without admitting execution, clearing quarantine or maintaining history. A configured retained-quota increase commits before migration and remains committed if a later stage fails. TTL/per-run changes and quota decreases are refused. Missing stores are never created."
	invalid := func(cmd *cobra.Command) error {
		return writeQueryFailure(cmd, jsonOutput, 2048, 2, &runstore.Error{Code: "invalid_argument", Message: "upgrade accepts only --json and the project selector"})
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return invalid(cmd)
		}
		return nil
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error { return invalid(cmd) })
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		target, err := runTarget(cmd)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, 2048, 1, &runstore.Error{Code: "storage_unavailable", Message: "cannot resolve selected run owner/configuration; check --project-dir"})
		}
		limit := target.cfg.Runs.MaxResponseBytes
		key, _, err := resolveKnowledgeKey(target)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, limit, 1, &runstore.Error{Code: "credential_unavailable", Message: "restore the selected matching credential; no fallback key was tried"})
		}
		path, err := config.ResolveRunDBPath(target.projectDir)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, limit, 1, &runstore.Error{Code: "storage_unavailable", Message: "selected run path is unavailable"})
		}
		cfg := target.cfg.Runs
		// Execute exactly once. Formatting must preserve separately committed policy
		// changes even when a later migration fails or response delivery is lost.
		result, err := runstore.Upgrade(cmd.Context(), path, key, runstore.PolicyConfig{TTLHours: cfg.TTLHours, MaxRunBytes: cfg.MaxRunBytes, MaxRetainedBytes: cfg.MaxRetainedBytes})
		code := 0
		if err != nil {
			code = 1
			var typed *runstore.Error
			if errors.As(err, &typed) {
				result.Error = typed
			} else {
				result.Error = &runstore.Error{Code: "schema_upgrade_failed", Message: "upgrade did not finish; inspect the current schema and policy before retrying; history was not discarded"}
			}
		}
		return writeRunResponse(cmd, limit, jsonOutput, code, func(int) (any, error) { return result, nil })
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit versioned schema and policy outcome, including retained quota growth on failure")
	return cmd
}
