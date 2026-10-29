package server

import (
	"context"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/runstore"
)

// inspectRuns deliberately bypasses executionService: normal attachment may
// reconcile, maintain storage and claim ownership. Inspection does none of those.
func (s *Server) inspectRuns(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
	options, err := runInspectionOptions(args)
	if err != nil {
		return nil, err
	}
	path, err := config.ResolveRunDBPath(s.projectDir)
	if err != nil {
		return nil, &runstore.Error{Code: "storage_unavailable", Message: "selected run path is unavailable"}
	}
	view, err := runstore.InspectRuns(ctx, path, s.knowledgeKey, options)
	if err != nil {
		return nil, err
	}
	// Capture once. Budget retries only format the same in-memory snapshot and
	// retain a continuation after the last displayed subject, as the CLI does.
	result, err := execution.Budget(s.config.Runs.MaxResponseBytes, func(n int) (*mcp.CallToolResult, error) {
		bounded := runstore.BoundInspection(view, n/1024)
		bounded.NextStep = inspectionMCPMessage(bounded.NextStep)
		return mcp.NewToolResultStructured(bounded,
			"Read-only inspection; unfinished records do not prove live processes or healthy admission."), nil
	}, nil)
	if err != nil {
		return nil, &runstore.Error{Code: "response_unavailable", Message: "Run inspection response unavailable."}
	}
	return result, nil
}

func runInspectionOptions(args map[string]any) (runstore.InspectionOptions, error) {
	var options runstore.InspectionOptions
	fields := map[string]*string{"run_id": &options.RunID, "receipt_id": &options.ReceiptID, "plan": &options.Plan, "cursor": &options.Cursor}
	for name, value := range args {
		if name == "action" || name == "limit" {
			continue
		}
		target, allowed := fields[name]
		if !allowed {
			return options, &runstore.Error{Code: "invalid_argument", Message: "unsupported argument for inspect"}
		}
		text, ok := value.(string)
		if !ok {
			return options, &runstore.Error{Code: "invalid_argument", Message: "inspect selectors and cursor must be strings"}
		}
		if name != "cursor" && text == "" {
			return options, &runstore.Error{Code: "invalid_argument", Message: "omit unused inspect selectors; supplied run_id, receipt_id and plan must be nonempty"}
		}
		*target = text
	}
	limit, err := integerArg(args, "limit", 16, 1, 64)
	if err != nil {
		return options, &runstore.Error{Code: "invalid_argument", Message: err.Error()}
	}
	options.Limit = int(limit)
	return options, options.Validate()
}

// Keep shared diagnostics and explicit terminal upgrade instructions, while
// inspection retries stay bound to this MCP server's selected owner.
// Changes to shared inspection wording must update these mappings and the
// transport tests together.
func inspectionMCPMessage(message string) string {
	return strings.NewReplacer(
		"restart capy runs inspect without the cursor", `retry capy_runs(action="inspect") with the same selector and no cursor`,
		"Select this physical owner with --project-dir, then run capy runs inspect.", `Use capy_runs(action="inspect") for this server's selected owner.`,
		"Select this physical owner with --project-dir to inspect again.", `Use capy_runs(action="inspect") with the same selector to inspect again.`,
		"capy runs inspect", `capy_runs(action="inspect")`,
		"run-id", "run_id", "receipt-id", "receipt_id",
	).Replace(message)
}
