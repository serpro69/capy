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
	var applyOptions runstore.RecoveryApplyOptions
	var jsonOutput, apply bool
	var timeout string
	cmd := &cobra.Command{Use: "recover", Short: "Plan or explicitly apply offline recovery while preserving history", SilenceErrors: true, SilenceUsage: true}
	cmd.Long = "Without --apply, preview the complete plan without changing history. Stop all sessions, readers, helpers and originating execution groups; run from an ordinary terminal, never inside capy_execute. Leases are not proof that old execution stopped. Review every page, then apply the exact plan with --confirm-stopped, --cleanup-basis groups_stopped|host_restarted and --reason TEXT. Apply records an operator disposition; original execution/cleanup uncertainty remains unchanged. It never replays a command. If originating execution cannot be established stopped (including on another host), do not apply. Reload compatible sessions after recovery. Planning and apply each default to five minutes; --cursor/--limit are preview-only."
	invalid := func(cmd *cobra.Command, message string) error {
		return writeQueryFailure(cmd, jsonOutput, 2048, 2, &runstore.Error{Code: "invalid_argument", Message: message})
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 0 {
			return invalid(cmd, "recovery accepts named flags only")
		}
		return nil
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error {
		return invalid(cmd, "invalid or unsupported recovery flag; see capy runs recover --help")
	})
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		duration, err := time.ParseDuration(timeout)
		if err != nil || duration <= 0 {
			return invalid(cmd, "timeout must be a positive finite Go duration, such as 15m")
		}
		options.Timeout = duration
		applyOptions.Timeout = duration
		if apply {
			if cmd.Flags().Changed("cursor") || cmd.Flags().Changed("limit") {
				return invalid(cmd, "--cursor and --limit are preview-only; apply covers the complete reviewed plan")
			}
			if err := applyOptions.Validate(); err != nil {
				return invalid(cmd, err.Error())
			}
		} else {
			for _, flag := range []string{"plan", "confirm-stopped", "cleanup-basis", "reason"} {
				if cmd.Flags().Changed(flag) {
					return invalid(cmd, "--plan, --confirm-stopped, --cleanup-basis and --reason require --apply")
				}
			}
			if err := options.Validate(); err != nil {
				return invalid(cmd, err.Error())
			}
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
		if apply {
			view, err := runstore.ApplyRecovery(cmd.Context(), path, key, applyOptions)
			status := 0
			if err != nil {
				status = 1
			}
			return writeRunResponse(cmd, limit, jsonOutput, status, func(n int) (any, error) { return runstore.BoundRecoveryApplication(view, n), nil })
		}
		view, err := runstore.PlanRecovery(cmd.Context(), path, key, options)
		status := 0
		if err != nil {
			status = 1
		}
		return writeRunResponse(cmd, limit, jsonOutput, status, func(n int) (any, error) { return runstore.BoundRecoveryPlan(view, n/1024), nil })
	}
	flags := cmd.Flags()
	flags.BoolVar(&jsonOutput, "json", false, "emit a versioned recovery plan or application result")
	flags.BoolVar(&apply, "apply", false, "apply the complete reviewed plan with an explicit operator attestation")
	flags.StringVar(&applyOptions.Plan, "plan", "", "exact reviewed plan token (requires --apply)")
	flags.BoolVar(&applyOptions.ConfirmStopped, "confirm-stopped", false, "confirm all store users and originating execution groups stopped (requires --apply)")
	flags.StringVar(&applyOptions.Basis, "cleanup-basis", "", "operator-established groups_stopped or host_restarted (requires --apply)")
	flags.StringVar(&applyOptions.Explanation, "reason", "", "explanation, 1–1024 UTF-8 bytes after normalization/redaction (requires --apply)")
	flags.StringVar(&options.Cursor, "cursor", "", "review the next page; the complete snapshot is revalidated")
	flags.IntVar(&options.Limit, "limit", 16, "maximum displayed subjects (1–64)")
	flags.StringVar(&timeout, "timeout", runstore.DefaultRecoveryTimeout.String(), "total offline planning/apply budget (positive finite duration)")
	return cmd
}
