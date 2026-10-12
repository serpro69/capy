package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/security"
	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertBatchBudget(t *testing.T, result *mcp.CallToolResult) {
	t.Helper()
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(encoded), 81920)
	assert.True(t, utf8.ValidString(resultText(result)))
	assert.NotContains(t, resultText(result), "batch response truncated", "normal formatting must reserve its own notices")
}

func TestBatchExecute_OptionalQueries(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"omitted", map[string]any{}},
		{"empty", map[string]any{"queries": []any{}}},
		{"empty typed", map[string]any{"queries": []string{}}},
		{"serialized empty", map[string]any{"queries": "[]", "query_scope": "global"}},
		{"serialized strings", map[string]any{"queries": `["marker"]`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t, nil)
			tc.args["commands"] = []any{"printf marker"}
			r := callBatch(t, srv, tc.args)
			require.False(t, r.IsError, resultText(r))
			assertBatchBudget(t, r)
			assert.Contains(t, resultText(r), "Batch source: \"batch:cmd_1\"")
			assert.Contains(t, resultText(r), "## Commands")
			assert.Contains(t, resultText(r), "## Indexed Sections")
			sources, err := srv.getStore().ListSources()
			require.NoError(t, err)
			require.Len(t, sources, 1)
			if tc.name != "serialized strings" {
				assert.Equal(t, 0, sources[0].AccessCount, "indexing-only must not retrieve")
				assert.Contains(t, resultText(r), "Searched 0 queries")
			}
		})
	}
}

func TestBatchExecute_InvalidSearchBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		value any
	}{
		{"null queries", "queries", nil}, {"number queries", "queries", 3},
		{"object queries", "queries", map[string]any{}}, {"malformed JSON", "queries", "["},
		{"serialized null", "queries", "null"}, {"serialized object", "queries", "{}"},
		{"mixed array", "queries", []any{"ok", 1}}, {"null item", "queries", []any{nil}},
		{"serialized mixed", "queries", `["ok",null]`},
		{"null scope", "query_scope", nil}, {"empty scope", "query_scope", ""},
		{"unknown scope", "query_scope", "vault"}, {"numeric scope", "query_scope", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t, nil)
			r := callBatch(t, srv, map[string]any{
				"commands": []any{"printf bad > spawned"}, tc.field: tc.value,
			})
			require.True(t, r.IsError)
			assert.Contains(t, resultText(r), tc.field)
			assert.NoFileExists(t, filepath.Join(srv.projectDir, "spawned"))
			assert.NoFileExists(t, srv.config.Store.Path)
			assert.Zero(t, srv.stats.Snapshot().BytesSandboxed)
			assert.Empty(t, srv.stats.Snapshot().Calls)
			assertBatchBudget(t, r)
		})
	}
}

func TestBatchExecute_GlobalKnowledgeScope(t *testing.T) {
	srv := newTestServer(t, nil)
	for _, fixture := range []struct {
		label string
		kind  store.SourceKind
	}{
		{"durable reference", store.KindDurable},
		{"earlier output", store.KindEphemeral},
		{"legacy session", store.KindSession},
	} {
		_, err := srv.getStore().Index("# Equal title\n\nprovenancemarker shared evidence", fixture.label, "markdown", fixture.kind)
		require.NoError(t, err)
	}
	r := callBatch(t, srv, map[string]any{
		"commands": []any{map[string]any{"label": "Equal title", "command": "printf 'provenancemarker current output'"}},
		"queries":  []any{"provenancemarker"}, "query_scope": "global",
	})
	require.False(t, r.IsError, resultText(r))
	queryText := strings.SplitN(resultText(r), "## provenancemarker", 2)
	require.Len(t, queryText, 2)
	for _, source := range []string{"durable reference", "earlier output", "batch:Equal title"} {
		assert.Contains(t, queryText[1], "Source: "+source)
	}
	assert.NotContains(t, queryText[1], "legacy session")
	assert.Nil(t, srv.vault, "batch global search must not even initialize vault access")
	assert.Contains(t, resultText(r), "Query scope: global (knowledge only)")
	assertBatchBudget(t, r)
}

func TestBatchExecute_ProvenanceAndAccounting(t *testing.T) {
	for _, concurrency := range []int{1, 2} {
		t.Run(fmt.Sprintf("workers%d", concurrency), func(t *testing.T) {
			srv := newTestServer(t, nil)
			t.Setenv("CAPY_VAULT_KEY", "")
			secret := "ghp_" + strings.Repeat("a", 36)
			command := "cat <<'EOF'\nactualoutput\nEOF\n# provenanceunique " + secret + " ``` # Fake\n" +
				"# " + strings.Repeat("界", 250) + "\nprintf tailmarker > complete-command\nprintf err >&2"
			commands := []any{map[string]any{"label": "Safe\n# Injected", "command": command}, "true"}
			r := callBatch(t, srv, map[string]any{"commands": commands, "concurrency": concurrency})
			require.False(t, r.IsError, resultText(r))
			assertBatchBudget(t, r)
			assert.NotContains(t, resultText(r), secret)
			tail, err := os.ReadFile(filepath.Join(srv.projectDir, "complete-command"))
			require.NoError(t, err)
			assert.Equal(t, "tailmarker", string(tail), "preview truncation must not truncate execution")
			sources, err := srv.getStore().ListSources()
			require.NoError(t, err)
			require.Len(t, sources, 1)
			chunks, err := srv.getStore().GetChunksBySource(sources[0].ID)
			require.NoError(t, err)
			require.Len(t, chunks, 2, "command delimiters cannot manufacture provenance sections")
			parts := strings.SplitN(chunks[0].Content, "\n\n", 2)
			require.Len(t, parts, 2)
			preview := strings.TrimPrefix(parts[0], "Command: ")
			assert.LessOrEqual(t, len(preview), 500)
			assert.True(t, utf8.ValidString(preview))
			assert.True(t, strings.HasSuffix(preview, "…"))
			assert.NotContains(t, preview, secret)
			assert.NotContains(t, preview, "\n")
			assert.Equal(t, "actualoutput\nerr", parts[1])
			hits, err := srv.getStore().SearchWithFallback("provenanceunique", 3, store.SearchOptions{Source: sources[0].Label, SourceMatchMode: "exact"})
			require.NoError(t, err)
			require.NotEmpty(t, hits, "stored previews must be retrievable without the vault")
			assert.Contains(t, hits[0].Content, "Command:")
			stats := srv.stats.Snapshot()
			assert.Equal(t, int64(len("actualoutput\nerr")), stats.BytesSandboxed)
			assert.Greater(t, stats.BytesIndexed, stats.BytesSandboxed)
			assert.Equal(t, int64(len(resultText(r))), stats.BytesReturned["capy_batch_execute"])
		})
	}
}

func TestBatchExecute_LargeInventoryUnchanged(t *testing.T) {
	srv := newTestServer(t, nil)
	// Admit all 8,000 headings through the existing configurable capture limit;
	// otherwise the executor's independent 100 KiB head/tail cap fires first.
	srv.executor = executor.NewExecutor(srv.projectDir, 1024*1024)
	var output strings.Builder
	for i := range 8000 {
		fmt.Fprintf(&output, "# Heading %04d\n\nsectionmarker%04d content\n\n", i, i)
	}
	require.NoError(t, os.WriteFile(filepath.Join(srv.projectDir, "headings.md"), []byte(output.String()), 0o600))
	args := map[string]any{"commands": []any{map[string]any{"label": "Headings", "command": "cat headings.md"}}}
	for _, query := range []bool{false, true} {
		for _, repeat := range []bool{false, true} {
			if query {
				args["queries"] = []any{"sectionmarker7999"}
			}
			r := callBatch(t, srv, args)
			require.False(t, r.IsError, resultText(r))
			assertBatchBudget(t, r)
			if query || repeat {
				assert.Contains(t, resultText(r), "Unchanged; retained 8001 sections")
			} else {
				assert.Contains(t, resultText(r), "Indexed 8001 sections")
			}
			assert.Contains(t, resultText(r), "Sections omitted: 7961")
			assert.Contains(t, resultText(r), "source: \"batch:Headings\"")
			if query {
				assert.Contains(t, resultText(r), "sectionmarker7999 content")
			}
		}
	}
}

func TestBatchExecute_SerializedResponseBudget(t *testing.T) {
	srv := newTestServer(t, nil)
	content := "# " + strings.Repeat("<&界", 200) + "\n\nneedle " + strings.Repeat("<&\\\"界", 12000)
	require.NoError(t, os.WriteFile(filepath.Join(srv.projectDir, "output.md"), []byte(content), 0o600))
	var commands []any
	for i := range 20 {
		command := "true # " + strings.Repeat("<&\\\"界", 200)
		if i == 0 {
			command = "cat output.md"
		}
		commands = append(commands, map[string]any{"label": strings.Repeat("<&\\\"界", 200), "command": command})
	}
	queries := make([]any, 200)
	for i := range queries {
		queries[i] = "needle"
	}
	r := callBatch(t, srv, map[string]any{"commands": commands, "queries": queries})
	require.False(t, r.IsError, resultText(r))
	assertBatchBudget(t, r)
	assert.Contains(t, resultText(r), "Query outputs omitted:")
	assert.NotContains(t, resultText(r), "Query outputs omitted: 0.")
	assert.NotContains(t, resultText(r), "Commands omitted: 0.")
	text := resultText(r)
	commandInventory := strings.SplitN(strings.SplitN(text, "## Commands", 2)[1], "## Indexed Sections", 2)[0]
	assert.LessOrEqual(t, jsonTextBytes("## Commands"+commandInventory), 4096)
	sectionInventory := strings.SplitN(strings.SplitN(text, "## Indexed Sections", 2)[1], "## needle", 2)[0]
	assert.LessOrEqual(t, jsonTextBytes("## Indexed Sections"+sectionInventory), 8192)
	// Oversized queries also have bounded display text, including no-hit paths.
	r = callBatch(t, srv, map[string]any{"commands": []any{"true"}, "queries": []any{strings.Repeat("<&\\\"界", 10000)}})
	require.False(t, r.IsError)
	assertBatchBudget(t, r)
}

func TestBatchExecute_ProvenanceCountsTowardSourceLimit(t *testing.T) {
	srv := newTestServer(t, nil)
	srv.config.Store.MaxSourceBytes = 100
	r := callBatch(t, srv, map[string]any{"commands": []any{"printf output # " + strings.Repeat("x", 200)}})
	require.True(t, r.IsError)
	assert.Contains(t, resultText(r), "source too large")
	stats := srv.stats.Snapshot()
	assert.Equal(t, int64(6), stats.BytesSandboxed)
	assert.Greater(t, stats.BytesIndexed, int64(100))
	sources, err := srv.getStore().ListSources()
	require.NoError(t, err)
	assert.Empty(t, sources, "never silently drop output to accommodate provenance")
}

func TestBatchExecute_LargePolicyErrorBudget(t *testing.T) {
	pattern := "Bash(" + strings.Repeat("<", 90000) + ")"
	srv := newTestServer(t, []security.SecurityPolicy{{Deny: []string{pattern}}})
	r := callBatch(t, srv, map[string]any{"commands": []any{strings.Repeat("<", 90000)}})
	require.True(t, r.IsError)
	assert.Contains(t, resultText(r), "matches deny pattern")
	assert.Contains(t, resultText(r), "batch response truncated")
	encoded, err := json.Marshal(r)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(encoded), 81920)
	assert.Zero(t, srv.stats.Snapshot().BytesSandboxed)
}

func TestBatchExecute_LongDistinctiveTerms(t *testing.T) {
	srv := newTestServer(t, nil)
	term := "distinctiveidentifier" + strings.Repeat("x", 4000)
	content := "# First\n\n" + term + "\n\n# Second\n\n" + term +
		"\n\n# Third\n\nother content\n\n# Fourth\n\nmore content\n"
	require.NoError(t, os.WriteFile(filepath.Join(srv.projectDir, "terms.md"), []byte(content), 0o600))
	r := callBatch(t, srv, map[string]any{"commands": []any{"cat terms.md"}})
	require.False(t, r.IsError, resultText(r))
	assertBatchBudget(t, r)
	assert.Contains(t, resultText(r), "Searchable terms for follow-up:")
	assert.Contains(t, resultText(r), "distinctiveidentifier")
	assert.NotContains(t, resultText(r), term)
	hits, err := srv.getStore().SearchWithFallback(term, 2, store.SearchOptions{Source: "batch:cmd_1"})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	assert.Contains(t, hits[0].Content, term, "only display text may truncate the identifier")
}

func TestBatchExecute_SourceSelectorJSON(t *testing.T) {
	srv := newTestServer(t, nil)
	label := "control\x00quote\"slash\\界<&"
	r := callBatch(t, srv, map[string]any{
		"commands": []any{map[string]any{"label": label, "command": "printf selectorcontent"}},
	})
	require.False(t, r.IsError, resultText(r))
	assertBatchBudget(t, r)
	selectorLine := strings.SplitN(strings.SplitN(resultText(r), "Batch source: ", 2)[1], "\n", 2)[0]
	var decoded string
	require.NoError(t, json.Unmarshal([]byte(selectorLine), &decoded))
	assert.Equal(t, "batch:"+label, decoded)
	hits, err := srv.getStore().SearchWithFallback("selectorcontent", 2, store.SearchOptions{Source: decoded, SourceMatchMode: "exact"})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
}
