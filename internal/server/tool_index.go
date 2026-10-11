package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/knowledge"
	"github.com/serpro69/capy/internal/store"
)

func (s *Server) handleIndex(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	content := req.GetString("content", "")
	path := req.GetString("path", "")
	source := req.GetString("source", "")
	options, err := directoryArgs(req.GetArguments())
	if err != nil {
		return errorResult(err.Error()), nil
	}

	if content == "" && path == "" {
		return errorResult("Either content or path must be provided"), nil
	}

	var result *store.IndexResult
	if path != "" && content == "" {
		if s.readPolicyErr != nil {
			return errorResult(fmt.Sprintf("File access blocked by security policy: %v", s.readPolicyErr)), nil
		}
		ingestion := knowledge.NewIndexer(s.projectDir, s.config.Store.MaxSourceBytes, s.readPolicy, s.getStore())
		outcome, err := ingestion.IndexPath(ctx, path, source, options)
		if outcome.Directory != nil {
			s.stats.AddBytesIndexed(outcome.Directory.BytesIndexed)
			text := directorySummary(*outcome.Directory, err)
			response := textResult(text)
			response.IsError = err != nil
			return s.trackToolResponse("capy_index", response), nil
		}
		if outcome.File != nil {
			s.stats.AddBytesIndexed(outcome.File.BytesIndexed)
		}
		if err != nil {
			return errorResult(fmt.Sprintf("Failed to read file or index it: %v", err)), nil
		}
		result = outcome.File.Result
	} else {
		// Supplying both still indexes inline content, but the supplied path
		// must pass the existing Read policy even though it is not opened.
		if path != "" {
			if denied := s.checkFilePathDenyPolicy(path); denied != nil {
				return denied, nil
			}
		}
		if source == "" {
			source = "indexed-content"
		}
		s.stats.AddBytesIndexed(int64(len(content)))
		result, err = s.getStore().Index(content, source, "", store.KindDurable)
	}
	if err != nil {
		return errorResult(fmt.Sprintf("Index error: %v", err)), nil
	}

	text := fmt.Sprintf(
		"Indexed %d sections (%d with code) from: %s\nUse search(queries: [\"...\"]) to query this content. Use source: %q to scope results.",
		result.TotalChunks, result.CodeChunks, result.Label, result.Label,
	)
	return s.trackToolResponse("capy_index", textResult(text)), nil
}

func directoryArgs(args map[string]any) (knowledge.DirectoryOptions, error) {
	var options knowledge.DirectoryOptions
	for _, field := range []struct {
		name     string
		dest     **int
		min, max int
	}{{"max_depth", &options.MaxDepth, 0, 20}, {"max_files", &options.MaxFiles, 1, 1000}} {
		value, present := args[field.name]
		if !present {
			continue
		}
		var n float64
		switch v := value.(type) {
		case float64:
			n = v
		case int:
			n = float64(v)
		default:
			return options, fmt.Errorf("%s must be an integer in %d–%d", field.name, field.min, field.max)
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < float64(field.min) || n > float64(field.max) {
			return options, fmt.Errorf("%s must be an integer in %d–%d", field.name, field.min, field.max)
		}
		valueInt := int(n)
		*field.dest = &valueInt
	}
	for _, field := range []struct {
		name string
		dest *[]string
	}{{"extensions", &options.Extensions}, {"include", &options.Include}, {"exclude", &options.Exclude}} {
		value, present := args[field.name]
		if !present {
			continue
		}
		// Preserve serialized-array robustness without dropping invalid elements.
		if encoded, ok := value.(string); ok {
			if err := json.Unmarshal([]byte(encoded), &value); err != nil {
				return options, fmt.Errorf("%s must be an array of strings", field.name)
			}
		}
		switch values := value.(type) {
		case []string:
			*field.dest = values
		case []any:
			for _, item := range values {
				str, ok := item.(string)
				if !ok {
					return options, fmt.Errorf("%s must be an array of strings", field.name)
				}
				*field.dest = append(*field.dest, str)
			}
		default:
			return options, fmt.Errorf("%s must be an array of strings", field.name)
		}
	}
	respect, err := boolArg(args, "respect_gitignore", true)
	if err != nil {
		return options, err
	}
	options.RespectGitignore = &respect
	return options, options.Validate()
}

func directorySummary(out knowledge.DirectoryResult, err error) string {
	status := "complete"
	if !out.Complete {
		status = "incomplete (limit reached)"
	}
	if err != nil {
		status = "partial error (committed files retained)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Directory scan: %s\nRoot: %q\n", status, out.Root)
	fmt.Fprintf(&b, "Indexed: %d; unchanged: %d; denied: %d; skipped: %d; failed: %d\n", out.Indexed, out.Unchanged, out.Denied, out.Skipped, out.Failed)
	fmt.Fprintf(&b, "Visited entries: %d; file attempts: %d; bytes read: %d; bytes submitted: %d; retained sections: %d\n", out.Visited, out.Attempts, out.BytesRead, out.BytesIndexed, out.Chunks)
	fmt.Fprintf(&b, "Git ignores: %q\n", out.GitIgnoreStatus)
	if len(out.Limits) > 0 {
		fmt.Fprintf(&b, "Limits reached: %s\n", strings.Join(out.Limits, ", "))
	}
	for _, detail := range out.Failures {
		fmt.Fprintf(&b, "Failure: %s\n", detail)
	}
	if out.OmittedFailures > 0 {
		fmt.Fprintf(&b, "%d further failures omitted.\n", out.OmittedFailures)
	}
	if err != nil && out.Failed == 0 {
		fmt.Fprintf(&b, "Error: %.1024q\n", err.Error())
	}
	selector := out.Root
	if out.SourcePrefix != "" {
		selector = out.SourcePrefix + ":"
	}
	fmt.Fprintf(&b, "Use search(queries: [\"...\"], source: %q). Removed or excluded files remain indexed until explicit cleanup.", selector)
	return b.String()
}
