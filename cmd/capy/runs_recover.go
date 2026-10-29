package main

import (
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/spf13/cobra"
)

func newRunsRecoverCmd() *cobra.Command {
	options := runstore.RecoveryPlanOptions{Limit: 16, Timeout: runstore.DefaultRecoveryTimeout}
	var jsonOutput bool
	var timeout string
	cmd := &cobra.Command{Use: "recover", Short: "Preview a complete offline recovery plan without changing history", SilenceErrors: true, SilenceUsage: true}
	cmd.Long = "Preview only. Stop all sessions, readers, helpers and originating execution groups; run from an ordinary terminal, never inside capy_execute. Planning takes exclusive leases, authenticates schema 10 and inventories all relevant evidence without claiming owners, migrating, maintaining, executing or writing audit records. Leases are not proof that old execution stopped. No recovery application is available in this binary. Each cursor page validates the full current snapshot; timeout defaults to five minutes."
	invalid := func(cmd *cobra.Command, message string) error {
		return writeQueryFailure(cmd, jsonOutput, 2048, 2, &runstore.Error{Code: "invalid_argument", Message: message})
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 0 {
			return invalid(cmd, "recovery planning accepts named flags only")
		}
		return nil
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error {
		return invalid(cmd, "unsupported recovery flag; this command previews plans only")
	})
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		duration, err := time.ParseDuration(timeout)
		if err != nil || duration <= 0 {
			return invalid(cmd, "timeout must be a positive finite Go duration, such as 15m")
		}
		options.Timeout = duration
		if err := options.Validate(); err != nil {
			return invalid(cmd, err.Error())
		}
		target, err := runTarget(cmd)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, 2048, 1, &runstore.Error{Code: "storage_unavailable", Message: "cannot resolve selected run owner/configuration; check --project-dir"})
		}
		limit := target.cfg.Runs.MaxResponseBytes
		key, _, err := resolveKnowledgeKey(target)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, limit, 1, &runstore.Error{Code: "credential_unavailable", Message: "restore the selected matching credential; no fallback key was tried", Detail: execution.SafeMessage(err.Error(), 512)})
		}
		path, err := config.ResolveRunDBPath(target.projectDir)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, limit, 1, &runstore.Error{Code: "storage_unavailable", Message: "selected run path is unavailable"})
		}
		// Compute once, before formatting. Budget retries must neither rescan nor
		// reacquire administrative leases, even if output delivery later fails.
		view, err := runstore.PlanRecovery(cmd.Context(), path, key, options)
		status := 0
		if err != nil {
			status = 1
		}
		return writeRunResponse(cmd, limit, jsonOutput, status, func(n int) (any, error) { return runstore.BoundRecoveryPlan(view, n/1024), nil })
	}
	flags := cmd.Flags()
	flags.BoolVar(&jsonOutput, "json", false, "emit a versioned recovery plan")
	flags.StringVar(&options.Cursor, "cursor", "", "review the next page; the complete snapshot is revalidated")
	flags.IntVar(&options.Limit, "limit", 16, "maximum displayed subjects (1–64)")
	flags.StringVar(&timeout, "timeout", runstore.DefaultRecoveryTimeout.String(), "total offline inventory budget (positive finite duration)")
	return cmd
}
