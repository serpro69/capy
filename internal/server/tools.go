package server

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/serpro69/capy/internal/executor"
)

// registerTools registers all 9 capy MCP tools on the server.
func (s *Server) registerTools() {
	runtimes := s.executor.Runtimes()
	langList := formatRuntimeList(runtimes)

	s.mcpServer.AddTools(
		mcpserver.ServerTool{
			Tool:    toolExecute(langList),
			Handler: s.handleExecute,
		},
		mcpserver.ServerTool{
			Tool:    toolExecuteFile(langList),
			Handler: s.handleExecuteFile,
		},
		mcpserver.ServerTool{
			Tool:    toolIndex(),
			Handler: s.handleIndex,
		},
		mcpserver.ServerTool{
			Tool:    toolSearch(),
			Handler: s.handleSearch,
		},
		mcpserver.ServerTool{
			Tool:    toolFetchAndIndex(),
			Handler: s.handleFetchAndIndex,
		},
		mcpserver.ServerTool{
			Tool:    toolBatchExecute(),
			Handler: s.handleBatchExecute,
		},
		mcpserver.ServerTool{
			Tool:    toolStats(),
			Handler: s.handleStats,
		},
		mcpserver.ServerTool{
			Tool:    toolDoctor(),
			Handler: s.handleDoctor,
		},
		mcpserver.ServerTool{
			Tool:    toolCleanup(),
			Handler: s.handleCleanup,
		},
	)
}

func formatRuntimeList(runtimes map[executor.Language]string) string {
	var langs []string
	for lang := range runtimes {
		langs = append(langs, string(lang))
	}
	slices.Sort(langs)
	return strings.Join(langs, ", ")
}

// ─── Tool annotations ──────────────────────────────────────────────────────
//
// These hints tell Claude Code how to classify each tool:
//   readOnly:    only reads data, no side effects
//   destructive: can delete or overwrite user data
//   idempotent:  same input always produces the same result
//   openWorld:   accesses external systems (network, processes)

func boolPtr(v bool) *bool { return &v }

var (
	// Tools that execute arbitrary user code — can modify files and access network.
	annotationExecute = mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(false),
		DestructiveHint: boolPtr(true),
		IdempotentHint:  boolPtr(false),
		OpenWorldHint:   boolPtr(true),
	}
	// Read-only local queries — no side effects, no network.
	annotationReadOnly = mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(true),
		DestructiveHint: boolPtr(false),
		IdempotentHint:  boolPtr(true),
		OpenWorldHint:   boolPtr(false),
	}
	// Writes to local SQLite only — not destructive, no network.
	annotationLocalWrite = mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(false),
		DestructiveHint: boolPtr(false),
		IdempotentHint:  boolPtr(true),
		OpenWorldHint:   boolPtr(false),
	}
	// Fetches from network and writes to local SQLite.
	annotationFetch = mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(false),
		DestructiveHint: boolPtr(false),
		IdempotentHint:  boolPtr(true),
		OpenWorldHint:   boolPtr(true),
	}
	// Deletes from local SQLite — destructive but no network.
	annotationCleanup = mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(false),
		DestructiveHint: boolPtr(true),
		IdempotentHint:  boolPtr(false),
		OpenWorldHint:   boolPtr(false),
	}
)

// ─── Tool definitions ───────────────────────────────────────────────────────

func toolExecute(langList string) mcp.Tool {
	return mcp.NewTool("capy_execute",
		mcp.WithToolAnnotation(annotationExecute),
		mcp.WithDescription(fmt.Sprintf(
			"MANDATORY: Use for any command where output exceeds 20 lines. Execute code in a sandboxed subprocess. Only stdout enters context — raw data stays in the subprocess. Available: [%s]. PREFER THIS OVER BASH for: API calls (gh, curl, aws), test runners (npm test, pytest), git queries (git log, git diff), data processing, and ANY CLI command that may produce large output. Bash should only be used for file mutations, git writes, and navigation.",
			langList,
		)),
		mcp.WithString("language",
			mcp.Required(),
			mcp.Description("Runtime language"),
			mcp.Enum("javascript", "typescript", "python", "shell", "ruby", "go", "rust", "php", "perl", "r", "elixir"),
		),
		mcp.WithString("code",
			mcp.Required(),
			mcp.Description("Source code to execute. Use print/echo/console.log to output a summary to context."),
		),
		mcp.WithNumber("timeout",
			mcp.Description("Max execution time in ms (default: 30000)"),
		),
		mcp.WithBoolean("background",
			mcp.Description("Keep process running after timeout (for servers/daemons). Returns partial output without killing the process."),
		),
		mcp.WithString("intent",
			mcp.Description("What you're looking for in the output. When provided and output is large (>5KB), indexes output and returns section titles + previews instead of full content. Use search() to retrieve specific sections."),
		),
	)
}

func toolExecuteFile(langList string) mcp.Tool {
	return mcp.NewTool("capy_execute_file",
		mcp.WithToolAnnotation(annotationExecute),
		mcp.WithDescription(fmt.Sprintf(
			"Read a file and process it without loading contents into context. The file is read into a FILE_CONTENT variable inside the sandbox. Only your printed summary enters context. Available: [%s]. PREFER THIS OVER Read/cat for: log files, data files (CSV, JSON, XML), large source files for analysis.",
			langList,
		)),
		mcp.WithString("path",
			mcp.Required(),
			mcp.Description("Absolute file path or relative to project root"),
		),
		mcp.WithString("language",
			mcp.Required(),
			mcp.Description("Runtime language"),
			mcp.Enum("javascript", "typescript", "python", "shell", "ruby", "go", "rust", "php", "perl", "r", "elixir"),
		),
		mcp.WithString("code",
			mcp.Required(),
			mcp.Description("Code to process FILE_CONTENT. Print summary via console.log/print/echo/IO.puts."),
		),
		mcp.WithNumber("timeout",
			mcp.Description("Max execution time in ms (default: 30000)"),
		),
		mcp.WithString("intent",
			mcp.Description("What you're looking for in the output. When provided and output is large (>5KB), returns only matching sections via BM25 search."),
		),
	)
}

func toolIndex() mcp.Tool {
	return mcp.NewTool("capy_index",
		mcp.WithToolAnnotation(annotationLocalWrite),
		mcp.WithDescription("Index documentation or knowledge content into a searchable BM25 knowledge base. The full content does NOT stay in context — only a brief summary is returned. After indexing, use capy_search to retrieve specific sections on-demand."),
		mcp.WithString("content",
			mcp.Description("Raw text/markdown to index. Provide this OR path, not both."),
		),
		mcp.WithString("path",
			mcp.Description("File path to read and index (content never enters context). Provide this OR content."),
		),
		mcp.WithString("source",
			mcp.Description("Label for the indexed content (e.g., 'React useEffect docs')"),
		),
	)
}

func toolSearch() mcp.Tool {
	return mcp.NewTool("capy_search",
		mcp.WithToolAnnotation(annotationReadOnly),
		mcp.WithDescription("Search indexed content. Pass ALL search questions as queries array in ONE call. TIPS: 2-4 specific terms per query. Use 'source' to scope results."),
		mcp.WithArray("queries",
			mcp.Description("Array of search queries. Batch ALL questions in one call."),
			mcp.Items(map[string]any{"type": "string"}),
		),
		mcp.WithNumber("limit",
			mcp.Description("Results per query (default: 3)"),
		),
		mcp.WithString("source",
			mcp.Description("Filter to a specific indexed source (partial match)."),
		),
	)
}

func toolFetchAndIndex() mcp.Tool {
	return mcp.NewTool("capy_fetch_and_index",
		mcp.WithToolAnnotation(annotationFetch),
		mcp.WithDescription("Fetches URL content, converts HTML to markdown, indexes into searchable knowledge base, and returns a ~3KB preview. Full content stays in sandbox — use capy_search for deeper lookups. Content-type aware: HTML→markdown, JSON→chunked by key paths, text→indexed directly."),
		mcp.WithString("url",
			mcp.Required(),
			mcp.Description("The URL to fetch and index"),
		),
		mcp.WithString("source",
			mcp.Description("Label for the indexed content"),
		),
	)
}

func toolBatchExecute() mcp.Tool {
	return mcp.NewTool("capy_batch_execute",
		mcp.WithToolAnnotation(annotationExecute),
		mcp.WithDescription("Execute multiple commands in ONE call, auto-index all output, and search with multiple queries. Returns search results directly — no follow-up calls needed. THIS IS THE PRIMARY TOOL. Use this instead of multiple execute() calls."),
		mcp.WithArray("commands",
			mcp.Required(),
			mcp.Description("Commands to execute as a batch. Each command object has: label (section header), command (shell command to execute)."),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"label":   map[string]any{"type": "string", "description": "Section header for this command's output"},
					"command": map[string]any{"type": "string", "description": "Shell command to execute"},
				},
				"required": []any{"label", "command"},
			}),
		),
		mcp.WithArray("queries",
			mcp.Required(),
			mcp.Description("Search queries to extract information from indexed output. Use 5-8 comprehensive queries."),
			mcp.Items(map[string]any{"type": "string"}),
		),
		mcp.WithNumber("timeout",
			mcp.Description("Max execution time in ms (default: 60000)"),
		),
	)
}

func toolStats() mcp.Tool {
	return mcp.NewTool("capy_stats",
		mcp.WithToolAnnotation(annotationReadOnly),
		mcp.WithDescription("Returns context consumption statistics for the current session. Shows total bytes returned to context, breakdown by tool, call counts, estimated token usage, and context savings ratio."),
	)
}

func toolDoctor() mcp.Tool {
	return mcp.NewTool("capy_doctor",
		mcp.WithToolAnnotation(annotationReadOnly),
		mcp.WithDescription("Run diagnostics on capy installation. Checks version, available runtimes, FTS5, config, knowledge base status, hook registration, and MCP registration."),
	)
}

func toolCleanup() mcp.Tool {
	return mcp.NewTool("capy_cleanup",
		mcp.WithToolAnnotation(annotationCleanup),
		mcp.WithDescription("Clean up cold knowledge base entries. Removes sources that haven't been accessed and are older than max_age_days."),
		mcp.WithNumber("max_age_days",
			mcp.Description("Remove sources older than this many days (default: 30)"),
		),
		mcp.WithBoolean("dry_run",
			mcp.Description("Preview what would be removed without deleting (default: true)"),
		),
	)
}
