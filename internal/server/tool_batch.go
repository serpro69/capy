package server

import "fmt"

const maxBatchConcurrency = 8

func batchSearchArgs(args map[string]any) ([]string, string, error) {
	scope := "batch"
	if raw, present := args["query_scope"]; present {
		var ok bool
		scope, ok = raw.(string)
		if !ok || (scope != "batch" && scope != "global") {
			return nil, "", fmt.Errorf("invalid parameter %q: expected batch or global", "query_scope")
		}
	}
	raw, present := args["queries"]
	if !present {
		return nil, scope, nil
	}
	queries, err := stringArrayArg(raw, "queries")
	return queries, scope, err
}
