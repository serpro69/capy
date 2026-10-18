package main

import (
	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/security"
	"github.com/serpro69/capy/internal/store"
	"github.com/spf13/cobra"
)

func newRunsSearchCmd() *cobra.Command {
	options := runstore.SearchOptions{Limit: 5}
	var jsonOutput bool
	cmd := &cobra.Command{Use: "search", Short: "Search sanitized run output; repeat to resume indexing", SilenceErrors: true, SilenceUsage: true}
	invalid := func(cmd *cobra.Command) error {
		return writeQueryFailure(cmd, jsonOutput, 2048, 2, &runstore.Error{Code: "invalid_argument", Message: "search requires one --run-id or --batch-id, repeatable --query (1–8, at most 512 bytes each), and --limit 1–20"})
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return invalid(cmd)
		}
		return nil
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error { return invalid(cmd) })
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if options.Validate() != nil || (cmd.Flags().Changed("run-id") && options.RunID == "") || (cmd.Flags().Changed("batch-id") && options.BatchID == "") || (cmd.Flags().Changed("query-scope") && options.QueryScope == "") {
			return invalid(cmd)
		}
		target, err := runTarget(cmd)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, 2048, 1, err)
		}
		limit := target.cfg.Runs.MaxResponseBytes
		key, source, err := resolveKnowledgeKey(target)
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, limit, 1, err)
		}
		service := execution.NewCLIService(target.projectDir, key, target.cfg.Runs)
		page, err := service.Search(cmd.Context(), options)
		if err == nil && page.QueryScope == "global" {
			policy, policyErr := security.LoadReadPolicy(security.FilePolicyContext{ProjectDir: target.projectDir})
			if policyErr != nil {
				page.Partial = true
				page.KnowledgeError = &runstore.Error{Code: "knowledge_unavailable", Message: "knowledge Read policy unavailable; run results remain available"}
			} else {
				st := newKnowledgeStore(target, key, source.String())
				st.SetDenyChecker(func(path string) bool { return policy.Check(path) != nil })
				page = execution.AddKnowledge(cmd.Context(), page, options.Limit, func() *store.ContentStore { return st })
				if closeErr := st.Close(); closeErr != nil {
					page.Partial = true
					page.KnowledgeError = &runstore.Error{Code: "knowledge_unavailable", Message: "knowledge store close failed; run results remain available"}
				}
			}
		}
		closeErr := service.Close()
		if err != nil {
			return writeQueryFailure(cmd, jsonOutput, limit, 1, err)
		}
		if closeErr != nil {
			return writeQueryFailure(cmd, jsonOutput, limit, 1, closeErr)
		}
		code := 0
		if page.Error != nil || page.KnowledgeError != nil {
			code = 1
		}
		return writeRunResponse(cmd, limit, jsonOutput, code, func(n int) (any, error) { return execution.BoundSearch(page, n), nil })
	}
	flags := cmd.Flags()
	flags.StringVar(&options.RunID, "run-id", "", "captured run UUID")
	flags.StringVar(&options.BatchID, "batch-id", "", "batch UUID (requires an available batch manifest)")
	flags.StringArrayVar(&options.Queries, "query", nil, "search query (repeatable)")
	flags.StringVar(&options.QueryScope, "query-scope", "", "batch or global; requires batch-id")
	flags.IntVar(&options.Limit, "limit", 5, "maximum hits per query (1–20)")
	flags.BoolVar(&jsonOutput, "json", false, "emit the shared versioned JSON envelope")
	return cmd
}
