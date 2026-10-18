package main

import (
	"context"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/spf13/cobra"
)

func newRunsCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{Use: "runs", Short: "Recover captured runs without executing them again", SilenceErrors: true, SilenceUsage: true}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return writeQueryFailure(cmd, jsonOutput, 2048, 2, &runstore.Error{Code: "invalid_argument", Message: "choose runs status, read, list, search or reset --discard"})
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit a versioned JSON error for an invalid operation")
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error {
		return writeQueryFailure(cmd, jsonOutput, 2048, 2, &runstore.Error{Code: "invalid_argument", Message: "invalid or unsupported runs flag"})
	})
	for _, action := range []string{"status", "read", "list"} {
		cmd.AddCommand(newRunQueryCmd(action))
	}
	cmd.AddCommand(newRunsResetCmd())
	cmd.AddCommand(newRunsSearchCmd())
	return cmd
}
func newRunQueryCmd(action string) *cobra.Command {
	var id, batchID, cursor, stream, encoding string
	var states []string
	var limit, maxBytes int
	var startLine int64
	var raw, jsonOutput bool
	cmd := &cobra.Command{Use: action, Short: action + " retained runs", SilenceErrors: true, SilenceUsage: true}
	invalid := func(cmd *cobra.Command, message string) error {
		return writeQueryFailure(cmd, jsonOutput, 2048, 2, &runstore.Error{Code: "invalid_argument", Message: message})
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return invalid(cmd, "run queries accept named flags only")
		}
		return nil
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error { return invalid(cmd, "invalid or unsupported run-query flag") })
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if action == "list" && (limit < 1 || limit > 64) {
			return invalid(cmd, "limit must be between 1 and 64")
		}
		if action != "list" && batchID == "" && uuid.Validate(id) != nil {
			return invalid(cmd, "run-id must be a UUID")
		}
		if batchID != "" && (uuid.Validate(batchID) != nil || id != "") {
			return invalid(cmd, "batch-id must be a UUID and cannot be combined with run-id")
		}
		if len(cursor) > 1024 {
			return invalid(cmd, "cursor exceeds its limit")
		}
		if action == "read" {
			if !cmd.Flags().Changed("encoding") {
				encoding = "text"
				if raw {
					encoding = "base64"
				}
			}
			if raw && encoding != "base64" || !raw && encoding != "text" {
				return invalid(cmd, "raw reads require base64; sanitized reads require text")
			}
			if maxBytes < 1 || maxBytes > 65536 {
				return invalid(cmd, "max-bytes must be between 1 and 65536")
			}
			if cmd.Flags().Changed("start-line") && (startLine <= 0 || cursor != "" || stream == "merged") {
				return invalid(cmd, "start-line requires a positive line, an explicit single stream and no cursor")
			}
		}
		target, err := runTarget(cmd)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, 2048, 1, err)
		}
		key, _, err := resolveKnowledgeKey(target)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, target.cfg.Runs.MaxResponseBytes, 1, err)
		}
		service := execution.NewCLIService(target.projectDir, key, target.cfg.Runs)
		ctx := cmd.Context()
		// Budget rebuilding reads only; it never resubmits execution. Close after
		// building the selected envelope and before any output is written.
		var value any
		encode := encodeRunText
		if jsonOutput {
			encode = encodeRunJSON
		}
		value, err = execution.Budget(target.cfg.Runs.MaxResponseBytes, func(n int) (any, error) {
			if action == "status" && batchID != "" {
				result, err := service.BatchStatus(ctx, batchID)
				bounded := execution.BoundBatch(result, n)
				return runQueryResult{Version: 1, Kind: "query", Batch: &bounded}, err
			}
			return runQuery(ctx, service, action, id, runstore.ReadOptions{Stream: stream, Cursor: cursor, StartLine: startLine, MaxBytes: min(maxBytes, max(1, n)), Raw: raw}, runstore.ListOptions{BatchID: batchID, States: states, Cursor: cursor, Limit: min(limit, max(1, n/256))}, n)
		}, encode)
		closeErr := service.Close()
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, target.cfg.Runs.MaxResponseBytes, 1, err)
		}
		if closeErr != nil {
			return writeQueryFailure(cmd, jsonOutput, target.cfg.Runs.MaxResponseBytes, 1, closeErr)
		}
		return writeRunResponse(cmd, target.cfg.Runs.MaxResponseBytes, jsonOutput, 0, func(int) (any, error) { return value, nil })
	}
	flags := cmd.Flags()
	flags.BoolVar(&jsonOutput, "json", false, "emit the shared versioned JSON envelope")
	if action != "list" {
		flags.StringVar(&id, "run-id", "", "captured run UUID")
	}
	if action != "read" {
		flags.StringVar(&batchID, "batch-id", "", "serial batch UUID")
	}
	if action == "read" {
		flags.StringVar(&stream, "stream", "merged", "stdout, stderr or merged")
		flags.BoolVar(&raw, "raw", false, "explicitly request unsanitized bytes as base64")
		flags.StringVar(&encoding, "encoding", "", "text for sanitized output; base64 for raw")
		flags.StringVar(&cursor, "cursor", "", "opaque read continuation")
		flags.Int64Var(&startLine, "start-line", 0, "raw line number; requires stdout or stderr")
		flags.IntVar(&maxBytes, "max-bytes", 4096, "page payload bytes (1–65536; UTF-8 code points stay atomic)")
	}
	if action == "list" {
		flags.IntVar(&limit, "limit", 16, "maximum entries (1–64)")
		flags.StringArrayVar(&states, "state", nil, "filter by run state (repeatable)")
		flags.StringVar(&cursor, "cursor", "", "opaque list continuation")
	}
	return cmd
}
func runQuery(ctx context.Context, service *execution.Service, action, id string, read runstore.ReadOptions, list runstore.ListOptions, n int) (any, error) {
	switch action {
	case "status":
		result, err := service.Status(ctx, id)
		if err != nil {
			return nil, err
		}
		result = execution.BoundResult(result, n)
		return runQueryResult{Version: 1, Kind: "query", Run: &result}, nil
	case "read":
		page, err := service.Read(ctx, id, read)
		if err != nil {
			return nil, err
		}
		page.Run = execution.BoundResult(page.Run, n)
		return page, nil
	default:
		return service.List(ctx, list)
	}
}
