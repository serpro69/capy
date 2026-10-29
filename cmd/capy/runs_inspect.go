package main

import (
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/spf13/cobra"
)

func newRunsInspectCmd() *cobra.Command {
	var options runstore.InspectionOptions
	var jsonOutput bool
	cmd := &cobra.Command{Use: "inspect", Short: "Inspect recorded blockers without changing run history", SilenceErrors: true, SilenceUsage: true}
	cmd.Long = "Read-only diagnostics, including older blockers outside recent history. Pending records do not prove live processes or healthy admission. Inspection never creates a store, reconciles work, claims an owner or applies recovery. Legacy schemas have no audit epoch. Plan lookup finds retained applied receipts only; absence does not prove nonapplication."
	invalid := func(cmd *cobra.Command, message string) error {
		return writeQueryFailure(cmd, jsonOutput, 2048, 2, &runstore.Error{Code: "invalid_argument", Message: message})
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 0 {
			return invalid(cmd, "inspection accepts named flags only")
		}
		return nil
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error { return invalid(cmd, "invalid or unsupported inspection flag") })
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := options.Validate(); err != nil {
			return writeQueryFailure(cmd, jsonOutput, 2048, 2, err)
		}
		target, err := runTarget(cmd)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, 2048, 1, &runstore.Error{Code: "storage_unavailable", Message: "cannot resolve selected run owner/configuration; check --project-dir; no fallback store was inspected"})
		}
		key, _, err := resolveKnowledgeKey(target)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, target.cfg.Runs.MaxResponseBytes, 1, &runstore.Error{Code: "credential_unavailable", Message: "repair the selected credential and reload; no fallback key was tried"})
		}
		path, err := config.ResolveRunDBPath(target.projectDir)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, target.cfg.Runs.MaxResponseBytes, 1, &runstore.Error{Code: "storage_unavailable", Message: "selected run path is unavailable"})
		}
		// Capture once. Budget callbacks only shorten this in-memory snapshot.
		view, err := runstore.InspectRuns(cmd.Context(), path, key, options)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, target.cfg.Runs.MaxResponseBytes, 1, err)
		}
		return writeRunResponse(cmd, target.cfg.Runs.MaxResponseBytes, jsonOutput, 0, func(n int) (any, error) {
			return runstore.BoundInspection(view, n/1024), nil
		})
	}
	flags := cmd.Flags()
	flags.BoolVar(&jsonOutput, "json", false, "emit versioned JSON inspection metadata")
	flags.StringVar(&options.RunID, "run-id", "", "retained run UUID")
	flags.StringVar(&options.ReceiptID, "receipt-id", "", "recovery receipt UUID (not an incident UUID)")
	flags.StringVar(&options.Plan, "plan", "", "lookup a retained receipt by plan token; does not validate unapplied plans")
	flags.StringVar(&options.Cursor, "cursor", "", "inspection continuation; keep the same selector")
	flags.IntVar(&options.Limit, "limit", 16, "maximum subjects (1–64)")
	return cmd
}
