package hook

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGitPlatformURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantHost   string
		wantOwner  string
		wantRepo   string
		wantNumber string
		wantKind   string
		wantOK     bool
	}{
		{
			name:       "GitHub issue",
			url:        "https://github.com/serpro69/capy/issues/44",
			wantHost:   "github.com",
			wantOwner:  "serpro69",
			wantRepo:   "capy",
			wantNumber: "44",
			wantKind:   "issue",
			wantOK:     true,
		},
		{
			name:       "GitHub PR",
			url:        "https://github.com/serpro69/capy/pull/47",
			wantHost:   "github.com",
			wantOwner:  "serpro69",
			wantRepo:   "capy",
			wantNumber: "47",
			wantKind:   "pr",
			wantOK:     true,
		},
		{
			name:       "GitHub issue with fragment",
			url:        "https://github.com/serpro69/capy/issues/44#issuecomment-123",
			wantHost:   "github.com",
			wantOwner:  "serpro69",
			wantRepo:   "capy",
			wantNumber: "44",
			wantKind:   "issue",
			wantOK:     true,
		},
		{
			name:       "GitHub PR with subpath",
			url:        "https://github.com/serpro69/capy/pull/47/files",
			wantHost:   "github.com",
			wantOwner:  "serpro69",
			wantRepo:   "capy",
			wantNumber: "47",
			wantKind:   "pr",
			wantOK:     true,
		},
		{
			name:       "GitLab issue",
			url:        "https://gitlab.com/group/project/-/issues/123",
			wantHost:   "gitlab.com",
			wantNumber: "123",
			wantKind:   "issue",
			wantOK:     true,
		},
		{
			name:       "GitLab merge request",
			url:        "https://gitlab.com/group/project/-/merge_requests/456",
			wantHost:   "gitlab.com",
			wantNumber: "456",
			wantKind:   "mr",
			wantOK:     true,
		},
		{
			name:       "Bitbucket pull request",
			url:        "https://bitbucket.org/team/repo/pull-requests/78",
			wantHost:   "bitbucket.org",
			wantNumber: "78",
			wantKind:   "pr",
			wantOK:     true,
		},
		{
			name:       "Gitea issue",
			url:        "https://gitea.example.com/org/repo/issues/99",
			wantHost:   "gitea.example.com",
			wantNumber: "99",
			wantKind:   "issue",
			wantOK:     true,
		},
		{
			name:       "Gitea pull",
			url:        "https://gitea.example.com/org/repo/pulls/12",
			wantHost:   "gitea.example.com",
			wantNumber: "12",
			wantKind:   "pr",
			wantOK:     true,
		},
		{
			name:   "GitHub repo root — not an issue or PR",
			url:    "https://github.com/serpro69/capy",
			wantOK: false,
		},
		{
			name:   "GitHub issues listing — no number",
			url:    "https://github.com/serpro69/capy/issues",
			wantOK: false,
		},
		{
			name:   "gist URL — not an issue or PR",
			url:    "https://gist.github.com/serpro69/abc123",
			wantOK: false,
		},
		{
			name:   "non-git URL",
			url:    "https://docs.example.com/api/reference",
			wantOK: false,
		},
		{
			name:   "empty string",
			url:    "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := parseGitPlatformURL(tt.url)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.wantHost, info.Host)
				assert.Equal(t, tt.wantOwner, info.Owner)
				assert.Equal(t, tt.wantRepo, info.Repo)
				assert.Equal(t, tt.wantNumber, info.Number)
				assert.Equal(t, tt.wantKind, info.Kind)
			}
		})
	}
}

func TestIsGistURL(t *testing.T) {
	assert.True(t, isGistURL("https://gist.github.com/serpro69/abc123"))
	assert.True(t, isGistURL("https://gist.githubusercontent.com/serpro69/abc123/raw/file.txt"))
	assert.False(t, isGistURL("https://github.com/serpro69/capy/issues/44"))
	assert.False(t, isGistURL("https://docs.example.com"))
	assert.False(t, isGistURL(""))
}

func TestFetchGuidance(t *testing.T) {
	t.Run("GitHub issue URL returns guidance with gh issue view", func(t *testing.T) {
		guidance := fetchGuidance("https://github.com/serpro69/capy/issues/44")
		assert.Contains(t, guidance, "gh issue view 44 --repo serpro69/capy")
		assert.Contains(t, guidance, "BM25-fragment")
	})

	t.Run("GitHub PR URL returns guidance with gh pr view", func(t *testing.T) {
		guidance := fetchGuidance("https://github.com/serpro69/capy/pull/47")
		assert.Contains(t, guidance, "gh pr view 47 --repo serpro69/capy")
		assert.Contains(t, guidance, "BM25-fragment")
	})

	t.Run("GitLab issue returns generic guidance", func(t *testing.T) {
		guidance := fetchGuidance("https://gitlab.com/group/project/-/issues/123")
		assert.Contains(t, guidance, "issue")
		assert.Contains(t, guidance, "#123")
		assert.Contains(t, guidance, "platform")
		assert.NotContains(t, guidance, "gh issue view")
	})

	t.Run("GitLab MR returns generic guidance", func(t *testing.T) {
		guidance := fetchGuidance("https://gitlab.com/group/project/-/merge_requests/456")
		assert.Contains(t, guidance, "merge request")
		assert.Contains(t, guidance, "#456")
	})

	t.Run("gist URL returns softer guidance", func(t *testing.T) {
		guidance := fetchGuidance("https://gist.github.com/serpro69/abc123")
		assert.Contains(t, guidance, "gist")
		assert.Contains(t, guidance, "gh gist view")
		assert.NotContains(t, guidance, "gh issue view")
	})

	t.Run("generic URL returns empty string", func(t *testing.T) {
		guidance := fetchGuidance("https://docs.example.com/api/reference")
		assert.Empty(t, guidance)
	})
}

func TestWebFetchBlockMessage(t *testing.T) {
	t.Run("GitHub issue URL mentions gh issue view", func(t *testing.T) {
		msg := webFetchBlockMessage("https://github.com/serpro69/capy/issues/44")
		assert.Contains(t, msg, "gh issue view 44 --repo serpro69/capy")
		assert.Contains(t, msg, "GitHub issue")
	})

	t.Run("GitHub PR URL mentions gh pr view", func(t *testing.T) {
		msg := webFetchBlockMessage("https://github.com/serpro69/capy/pull/47")
		assert.Contains(t, msg, "gh pr view 47 --repo serpro69/capy")
		assert.Contains(t, msg, "GitHub pr")
	})

	t.Run("GitLab issue mentions platform CLI", func(t *testing.T) {
		msg := webFetchBlockMessage("https://gitlab.com/group/project/-/issues/123")
		assert.Contains(t, msg, "issue")
		assert.Contains(t, msg, "#123")
		assert.Contains(t, msg, "platform")
		assert.NotContains(t, msg, "gh issue view")
	})

	t.Run("generic URL mentions WebSearch and comprehension", func(t *testing.T) {
		msg := webFetchBlockMessage("https://docs.example.com/page")
		assert.Contains(t, msg, "WebSearch")
		assert.Contains(t, msg, "capy_fetch_and_index")
		assert.NotContains(t, msg, "gh issue view")
	})
}

func TestFetchAndIndexGitGuidanceHook(t *testing.T) {
	a := &testAdapter{}

	t.Run("GitHub issue URL produces FormatAllow guidance", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"tool_name":  "capy_fetch_and_index",
			"tool_input": map[string]any{"url": "https://github.com/serpro69/capy/issues/44"},
		})
		result, err := handlePreToolUse(input, a, nil, "/tmp")
		require.NoError(t, err)
		require.NotNil(t, result)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(result, &resp))
		assert.Equal(t, "context", resp["action"])
		ctx, _ := resp["additionalContext"].(string)
		assert.Contains(t, ctx, "gh issue view 44 --repo serpro69/capy")
	})

	t.Run("GitHub PR URL produces FormatAllow guidance", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"tool_name":  "capy_fetch_and_index",
			"tool_input": map[string]any{"url": "https://github.com/serpro69/capy/pull/47"},
		})
		result, err := handlePreToolUse(input, a, nil, "/tmp")
		require.NoError(t, err)
		require.NotNil(t, result)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(result, &resp))
		assert.Equal(t, "context", resp["action"])
		ctx, _ := resp["additionalContext"].(string)
		assert.Contains(t, ctx, "gh pr view 47 --repo serpro69/capy")
	})

	t.Run("GitLab MR URL produces FormatAllow guidance", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"tool_name":  "capy_fetch_and_index",
			"tool_input": map[string]any{"url": "https://gitlab.com/group/project/-/merge_requests/456"},
		})
		result, err := handlePreToolUse(input, a, nil, "/tmp")
		require.NoError(t, err)
		require.NotNil(t, result)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(result, &resp))
		assert.Equal(t, "context", resp["action"])
		ctx, _ := resp["additionalContext"].(string)
		assert.Contains(t, ctx, "merge request")
		assert.Contains(t, ctx, "#456")
	})

	t.Run("generic URL passes through with no guidance", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"tool_name":  "capy_fetch_and_index",
			"tool_input": map[string]any{"url": "https://docs.example.com/large-api-reference"},
		})
		result, err := handlePreToolUse(input, a, nil, "/tmp")
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("WebFetch with GitHub issue URL mentions gh in block message", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"tool_name":  "WebFetch",
			"tool_input": map[string]any{"url": "https://github.com/serpro69/capy/issues/44"},
		})
		result, err := handlePreToolUse(input, a, nil, "/tmp")
		require.NoError(t, err)
		require.NotNil(t, result)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(result, &resp))
		assert.Equal(t, "deny", resp["action"])
		reason, _ := resp["reason"].(string)
		assert.Contains(t, reason, "gh issue view 44 --repo serpro69/capy")
	})

	t.Run("WebFetch with generic URL gives comprehension guidance", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"tool_name":  "WebFetch",
			"tool_input": map[string]any{"url": "https://docs.example.com/page"},
		})
		result, err := handlePreToolUse(input, a, nil, "/tmp")
		require.NoError(t, err)
		require.NotNil(t, result)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(result, &resp))
		assert.Equal(t, "deny", resp["action"])
		reason, _ := resp["reason"].(string)
		assert.Contains(t, reason, "WebSearch")
	})
}
