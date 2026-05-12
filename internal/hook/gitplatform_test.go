package hook

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitPlatformBlockMessage(t *testing.T) {
	t.Run("GitHub issue URL returns block with gh issue view", func(t *testing.T) {
		msg := gitPlatformBlockMessage("https://github.com/serpro69/capy/issues/44")
		assert.Contains(t, msg, "gh issue view 44 --repo serpro69/capy")
		assert.Contains(t, msg, "Blocked")
		assert.Contains(t, msg, "BM25-fragment")
	})

	t.Run("GitHub PR URL returns block with gh pr view", func(t *testing.T) {
		msg := gitPlatformBlockMessage("https://github.com/serpro69/capy/pull/47")
		assert.Contains(t, msg, "gh pr view 47 --repo serpro69/capy")
		assert.Contains(t, msg, "Blocked")
	})

	t.Run("GitLab issue returns generic block", func(t *testing.T) {
		msg := gitPlatformBlockMessage("https://gitlab.com/group/project/-/issues/123")
		assert.Contains(t, msg, "issue")
		assert.Contains(t, msg, "#123")
		assert.Contains(t, msg, "platform")
		assert.NotContains(t, msg, "gh issue view")
	})

	t.Run("gist URL returns empty (not blocked)", func(t *testing.T) {
		assert.Empty(t, gitPlatformBlockMessage("https://gist.github.com/serpro69/abc123"))
	})

	t.Run("generic URL returns empty (not blocked)", func(t *testing.T) {
		assert.Empty(t, gitPlatformBlockMessage("https://docs.example.com/api/reference"))
	})
}

func TestGistGuidance(t *testing.T) {
	t.Run("gist URL returns soft guidance", func(t *testing.T) {
		guidance := gistGuidance("https://gist.github.com/serpro69/abc123")
		assert.Contains(t, guidance, "gist")
		assert.Contains(t, guidance, "gh gist view")
	})

	t.Run("non-gist URL returns empty", func(t *testing.T) {
		assert.Empty(t, gistGuidance("https://github.com/serpro69/capy/issues/44"))
		assert.Empty(t, gistGuidance("https://docs.example.com"))
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
	})

	t.Run("GitLab issue mentions platform CLI", func(t *testing.T) {
		msg := webFetchBlockMessage("https://gitlab.com/group/project/-/issues/123")
		assert.Contains(t, msg, "issue")
		assert.Contains(t, msg, "#123")
		assert.Contains(t, msg, "platform")
	})

	t.Run("generic URL mentions WebSearch", func(t *testing.T) {
		msg := webFetchBlockMessage("https://docs.example.com/page")
		assert.Contains(t, msg, "WebSearch")
		assert.Contains(t, msg, "capy_fetch_and_index")
	})
}

func TestFetchAndIndexHookBehavior(t *testing.T) {
	a := &testAdapter{}

	t.Run("GitHub issue URL produces FormatBlock", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"tool_name":  "capy_fetch_and_index",
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

	t.Run("GitHub PR URL produces FormatBlock", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"tool_name":  "capy_fetch_and_index",
			"tool_input": map[string]any{"url": "https://github.com/serpro69/capy/pull/47"},
		})
		result, err := handlePreToolUse(input, a, nil, "/tmp")
		require.NoError(t, err)
		require.NotNil(t, result)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(result, &resp))
		assert.Equal(t, "deny", resp["action"])
		reason, _ := resp["reason"].(string)
		assert.Contains(t, reason, "gh pr view 47 --repo serpro69/capy")
	})

	t.Run("GitLab MR URL produces FormatBlock", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"tool_name":  "capy_fetch_and_index",
			"tool_input": map[string]any{"url": "https://gitlab.com/group/project/-/merge_requests/456"},
		})
		result, err := handlePreToolUse(input, a, nil, "/tmp")
		require.NoError(t, err)
		require.NotNil(t, result)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(result, &resp))
		assert.Equal(t, "deny", resp["action"])
		reason, _ := resp["reason"].(string)
		assert.Contains(t, reason, "merge request")
	})

	t.Run("gist URL produces FormatAllow (soft guidance)", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"tool_name":  "capy_fetch_and_index",
			"tool_input": map[string]any{"url": "https://gist.github.com/serpro69/abc123"},
		})
		result, err := handlePreToolUse(input, a, nil, "/tmp")
		require.NoError(t, err)
		require.NotNil(t, result)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(result, &resp))
		assert.Equal(t, "context", resp["action"])
	})

	t.Run("generic URL passes through", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"tool_name":  "capy_fetch_and_index",
			"tool_input": map[string]any{"url": "https://docs.example.com/large-api-reference"},
		})
		result, err := handlePreToolUse(input, a, nil, "/tmp")
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("WebFetch with GitHub issue URL mentions gh", func(t *testing.T) {
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
}
