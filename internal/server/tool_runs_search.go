package server

import (
	"fmt"

	"github.com/serpro69/capy/internal/runstore"
)

func runSearchOptions(args map[string]any) (runstore.SearchOptions, error) {
	options := runstore.SearchOptions{Limit: 5}
	for name, value := range args {
		switch name {
		case "action", "limit":
		case "run_id", "batch_id", "query_scope":
			text, ok := value.(string)
			if !ok || text == "" {
				return options, fmt.Errorf("%s must be a nonempty string", name)
			}
			switch name {
			case "run_id":
				options.RunID = text
			case "batch_id":
				options.BatchID = text
			case "query_scope":
				options.QueryScope = text
			}
		case "queries":
			switch values := value.(type) {
			case []string:
				options.Queries = values
			case []any:
				if len(values) > 8 {
					return options, fmt.Errorf("too many queries")
				}
				for _, value := range values {
					text, ok := value.(string)
					if !ok {
						return options, fmt.Errorf("queries must contain strings")
					}
					options.Queries = append(options.Queries, text)
				}
			default:
				return options, fmt.Errorf("queries must be an array")
			}
			if len(options.Queries) == 0 {
				return options, fmt.Errorf("explicit queries must not be empty")
			}
		default:
			return options, fmt.Errorf("unsupported search argument")
		}
	}
	limit, err := integerArg(args, "limit", 5, 1, 20)
	if err != nil {
		return options, err
	}
	options.Limit = int(limit)
	return options, options.Validate()
}
