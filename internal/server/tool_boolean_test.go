package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBooleanArgs_RejectBeforeSideEffects(t *testing.T) {
	disableSSRFValidation(t)
	var requests atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, "unexpected fetch")
	}))
	t.Cleanup(ts.Close)

	tests := []struct {
		name    string
		flag    string
		handler func(*Server, context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
		args    map[string]any
	}{
		{
			name: "execute", flag: "background", handler: (*Server).handleExecute,
			args: map[string]any{"language": "shell", "code": "printf spawned > boolean-spawn"},
		},
		{
			name: "fetch", flag: "force", handler: (*Server).handleFetchAndIndex,
			args: map[string]any{"url": ts.URL},
		},
		{
			name: "batch fetch", flag: "force", handler: (*Server).handleFetchAndIndex,
			args: map[string]any{"requests": []any{map[string]any{"url": ts.URL}}},
		},
		{
			name: "search", flag: "all_projects", handler: (*Server).handleSearch,
			args: map[string]any{"query": "marker"},
		},
		{
			name: "knowledge only search", flag: "all_projects", handler: (*Server).handleSearch,
			args: map[string]any{"query": "marker", "source": "kept", "include_kinds": []any{"durable"}},
		},
		{
			name: "star search", flag: "all_projects", handler: (*Server).handleSearch,
			args: map[string]any{"query": "marker", "project": "*"},
		},
		{
			name: "vault search", flag: "all_projects", handler: (*Server).handleVaultSearch,
			args: map[string]any{"query": "marker", "project": "*"},
		},
	}
	for _, flag := range []string{"dry_run", "purge_ephemeral", "purge_session", "purge_all", "optimize", "vacuum"} {
		tests = append(tests, struct {
			name    string
			flag    string
			handler func(*Server, context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
			args    map[string]any
		}{name: "cleanup " + flag, flag: flag, handler: (*Server).handleCleanup})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, input := range invalidBoolInputs() {
				t.Run(input.name, func(t *testing.T) {
					srv := newTestServer(t, nil)
					args := map[string]any{tt.flag: input.value}
					for key, value := range tt.args {
						args[key] = value
					}
					req := mcp.CallToolRequest{}
					req.Params.Arguments = args
					result, err := tt.handler(srv, t.Context(), req)
					require.NoError(t, err)
					require.True(t, result.IsError, resultText(result))
					assert.Contains(t, resultText(result), `invalid parameter "`+tt.flag+`"`)
					assert.NoFileExists(t, filepath.Join(srv.projectDir, "boolean-spawn"))
					assert.NoFileExists(t, srv.config.Store.Path)
					assert.Nil(t, srv.store, "invalid calls must not initialize storage")
					assert.Nil(t, srv.vault, "invalid calls must not initialize the vault")
					assert.Empty(t, srv.stats.Snapshot().Calls)
					assert.Zero(t, srv.throttle.count, "invalid calls must not consume the search budget")
				})
			}
		})
	}
	assert.Zero(t, requests.Load(), "invalid force must not fetch any URL")
}

func TestBooleanArgs_CleanupPreservesContentOnInvalidFlags(t *testing.T) {
	srv := newTestServer(t, nil)
	callIndex(t, srv, map[string]any{"source": "kept", "content": "Retained marker for invalid cleanup."})
	for _, flag := range []string{"dry_run", "purge_ephemeral", "purge_session", "purge_all", "optimize", "vacuum"} {
		t.Run(flag, func(t *testing.T) {
			for _, input := range invalidBoolInputs() {
				t.Run(input.name, func(t *testing.T) {
					args := map[string]any{"dry_run": false, "purge_all": true, "optimize": true, "vacuum": true}
					args[flag] = input.value
					before := srv.stats.Snapshot()
					result := callCleanup(t, srv, args)
					require.True(t, result.IsError, resultText(result))
					assert.Contains(t, resultText(result), `invalid parameter "`+flag+`"`)
					assert.Equal(t, before, srv.stats.Snapshot(), "no purge-all stats reset or maintenance")
					sources, err := srv.getStore().ListSources()
					require.NoError(t, err)
					require.Len(t, sources, 1)
					assert.Equal(t, "kept", sources[0].Label)
				})
			}
		})
	}
}

func TestBooleanArgs_CleanupStringValues(t *testing.T) {
	for _, mode := range []string{"purge_all", "source"} {
		t.Run(mode, func(t *testing.T) {
			srv := newTestServer(t, nil)
			callIndex(t, srv, map[string]any{"source": "kept", "content": "Retained cleanup marker."})
			args := map[string]any{"purge_all": " TrUe "}
			if mode == "source" {
				args = map[string]any{"source": "kept", "purge_all": " FALSE "}
			}
			for _, dryRun := range []any{nil, " TrUe ", " false "} {
				if dryRun != nil {
					args["dry_run"] = dryRun
				}
				r := callCleanup(t, srv, args)
				require.False(t, r.IsError, resultText(r))
				sources, err := srv.getStore().ListSources()
				require.NoError(t, err)
				if dryRun == " false " {
					assert.Empty(t, sources, "literal string false intentionally performs eviction")
				} else {
					assert.Len(t, sources, 1, "omitted and string true preserve the preview default")
				}
			}
		})
	}
	for _, flag := range []string{"purge_ephemeral", "purge_session", "optimize", "vacuum"} {
		t.Run(flag, func(t *testing.T) {
			srv := newTestServer(t, nil)
			r := callCleanup(t, srv, map[string]any{flag: " TrUe "})
			require.False(t, r.IsError, resultText(r))
			if flag == "optimize" || flag == "vacuum" {
				assert.Contains(t, resultText(r), "complete")
			} else {
				// If string true were ignored, this would not conflict with source.
				r = callCleanup(t, srv, map[string]any{flag: " TrUe ", "source": "kept"})
				assert.True(t, r.IsError)
				assert.Contains(t, resultText(r), "source cannot be combined")
			}
		})
	}
}

func TestBooleanArgs_ExecuteBackgroundStrings(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "true detaches", value: " TrUe ", want: "process backgrounded"},
		{name: "false times out", value: " FaLsE ", want: "timed out after"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, nil)
			r := callTool(t, srv, map[string]any{
				"language": "shell", "code": "printf started; sleep 30", "timeout": 1000, "background": tt.value,
			})
			require.False(t, r.IsError, resultText(r))
			assert.Contains(t, resultText(r), tt.want)
		})
	}
}

func TestBooleanArgs_FetchForceStrings(t *testing.T) {
	disableSSRFValidation(t)
	var calls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "response number %d", calls.Add(1))
	}))
	t.Cleanup(ts.Close)
	for _, mode := range []string{"single", "batch"} {
		t.Run(mode, func(t *testing.T) {
			srv := newTestServer(t, nil)
			args := map[string]any{"url": ts.URL}
			if mode == "batch" {
				args = map[string]any{"requests": []any{map[string]any{"url": ts.URL}}}
			}
			start := calls.Load()
			for _, step := range []struct {
				name  string
				force string
				calls int64
			}{
				{name: "omitted", calls: 1},
				{name: "false caches", force: " FaLsE ", calls: 1},
				{name: "true fetches", force: " TrUe ", calls: 2},
			} {
				t.Run(step.name, func(t *testing.T) {
					if step.force != "" {
						args["force"] = step.force
					}
					r := callFetchAndIndex(t, srv, args)
					require.False(t, r.IsError, resultText(r))
					assert.Equal(t, start+step.calls, calls.Load())
				})
			}
		})
	}
}
