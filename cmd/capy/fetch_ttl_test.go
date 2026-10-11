package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchTTLStdio(t *testing.T) {
	bin := buildStdioCapy(t)
	const key = "synthetic-stdio-fetch-ttl-key-12345"
	const url = "http://127.0.0.1:1/ttl"
	const label = "ttl-fixture|" + url
	launch := func(t *testing.T, project string) *stdioProcess {
		t.Helper()
		p := startStdioProcess(t, stdioCommand{
			executable: bin, args: []string{"serve", "--project-dir", project},
			dir: t.TempDir(), env: stdioEnv(t, key), secrets: []string{key},
		})
		p.initialize(t)
		return p
	}

	t.Run("schema and validation before storage", func(t *testing.T) {
		project := stdioProject(t)
		p := launch(t, project)
		var listed struct {
			Tools []struct {
				Name        string
				InputSchema struct {
					Properties map[string]map[string]any
					Required   []string
				}
			}
		}
		require.NoError(t, p.request("tools/list", map[string]any{}, &listed))
		found := false
		for _, tool := range listed.Tools {
			if tool.Name != "capy_fetch_and_index" {
				continue
			}
			found = true
			prop := tool.InputSchema.Properties["ttl"]
			require.NotNil(t, prop)
			assert.Equal(t, "number", prop["type"])
			assert.Equal(t, float64(0), prop["minimum"])
			assert.Equal(t, float64(math.MaxInt64/int64(time.Millisecond)), prop["maximum"])
			assert.Equal(t, float64(1), prop["multipleOf"])
			assert.NotContains(t, prop, "default")
			assert.NotContains(t, tool.InputSchema.Required, "ttl")
		}
		require.True(t, found)
		for _, batch := range []bool{false, true} {
			for _, value := range []any{nil, -1, 0.5, "1.1", "5e2", "", true, []any{500}, map[string]any{}, float64(9223372036855), "9223372036854775808"} {
				args := map[string]any{"url": url, "ttl": value, "force": true}
				if batch {
					args = map[string]any{"requests": []any{map[string]any{"url": url}}, "ttl": value, "force": true}
				}
				isError, text := stdioBooleanCall(t, p, "capy_fetch_and_index", args)
				require.True(t, isError, text)
				assert.Contains(t, text, `invalid parameter "ttl"`)
				assert.NotContains(t, text, "loopback", "TTL validation precedes network admission")
				_, err := os.Stat(filepath.Join(project, "knowledge.db"))
				require.True(t, os.IsNotExist(err), "invalid TTL must not create storage")
			}
		}
		p.close(t)
	})

	t.Run("request scope and reopen", func(t *testing.T) {
		project := stdioProject(t)
		require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[store]\npath = 'knowledge.db'\n[store.cache]\nfetch_ttl_hours = 3\n"), 0o600))
		s := store.NewContentStore(filepath.Join(project, "knowledge.db"), project, 0, 0, store.WithEncryptionKey(key, "test"))
		t.Cleanup(func() { require.NoError(t, s.Close()) })
		validated := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
		_, err := s.IndexFetched("cached TTL marker", label, "plaintext", store.KindDurable, validated)
		require.NoError(t, err)
		require.NoError(t, s.Close())
		for range 2 {
			p := launch(t, project)
			for _, mode := range []string{"single", "batch", "serialized batch"} {
				args := map[string]any{"source": "ttl-fixture", "url": url, "kind": "durable"}
				if mode != "single" {
					requests := []any{map[string]any{"url": url, "source": "ttl-fixture"}}
					args = map[string]any{"requests": requests, "kind": "durable"}
					if mode == "serialized batch" {
						encoded, err := json.Marshal(requests)
						require.NoError(t, err)
						args["requests"] = string(encoded)
					}
				}
				for _, ttl := range []any{float64(7200000), "7200000"} {
					args["ttl"] = ttl
					text := p.tool(t, "capy_fetch_and_index", args)
					assert.Contains(t, strings.ToLower(text), "cache hit", mode)
					assert.Contains(t, text, "Fetch freshness: 7200000 ms")
				}
				for _, ttl := range []any{0, "0", 500} {
					args["ttl"] = ttl
					_, text := stdioBooleanCall(t, p, "capy_fetch_and_index", args)
					assert.NotContains(t, strings.ToLower(text), "cache hit")
					assert.Contains(t, text, "loopback")
					assert.NotContains(t, text, "retry the same")
				}
				args["ttl"], args["force"] = "7200000", "true"
				_, text := stdioBooleanCall(t, p, "capy_fetch_and_index", args)
				assert.Contains(t, text, "loopback")
				delete(args, "ttl")
				args["force"] = "false"
				text = p.tool(t, "capy_fetch_and_index", args)
				assert.Contains(t, strings.ToLower(text), "cache hit")
				assert.Contains(t, text, "Fetch freshness: 10800000 ms", "request overrides do not change configuration")
			}
			p.close(t)
			stdioCheckpointed(t, project)
			meta, err := s.GetSourceMeta(label)
			require.NoError(t, err)
			require.NotNil(t, meta.FetchValidatedAtMS)
			assert.Equal(t, validated.UnixMilli(), *meta.FetchValidatedAtMS, "failed bypasses and hits do not renew validation")
			require.NoError(t, s.Close())
		}
	})
}
