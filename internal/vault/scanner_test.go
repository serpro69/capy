package vault

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildJSONL renders entries as one compact JSON object per line (in-memory, so
// tests never depend on a real ~/.claude — CI has none).
func buildJSONL(t *testing.T, lines ...map[string]any) io.Reader {
	t.Helper()
	var sb strings.Builder
	for _, l := range lines {
		b, err := json.Marshal(l)
		require.NoError(t, err)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return strings.NewReader(sb.String())
}

// resultsByRole groups scan results by role for concise assertions.
func resultsByRole(results []ScanResult) map[string][]ScanResult {
	m := map[string][]ScanResult{}
	for _, r := range results {
		m[r.Role] = append(m[r.Role], r)
	}
	return m
}

func userLine(uuid, cwd, branch string, content any) map[string]any {
	return map[string]any{
		"type": "user", "uuid": uuid, "timestamp": "2026-05-01T10:00:00Z",
		"cwd": cwd, "gitBranch": branch,
		"message": map[string]any{"role": "user", "content": content},
	}
}

func assistantLine(uuid, msgID string, blocks []map[string]any) map[string]any {
	return map[string]any{
		"type": "assistant", "uuid": uuid, "timestamp": "2026-05-01T10:00:05Z",
		"message": map[string]any{"id": msgID, "role": "assistant", "content": blocks},
	}
}

func TestScanSession_ExtractsTextAndToolNames(t *testing.T) {
	r := buildJSONL(t,
		userLine("u1", "/home/user/proj", "main", "Read the config and fix the timeout"),
		assistantLine("a1", "msg_1", []map[string]any{
			{"type": "thinking", "thinking": "secret reasoning", "signature": "sig"},
			{"type": "text", "text": "Let me read the config first."},
			{"type": "tool_use", "id": "t1", "name": "Read", "input": map[string]any{"file_path": "/proj/config.toml"}},
			{"type": "tool_use", "id": "t2", "name": "Bash", "input": map[string]any{"command": "go test ./..."}},
		}),
	)

	out, err := ScanSession(r)
	require.NoError(t, err)

	assert.Equal(t, "/home/user/proj", out.CWD)
	assert.Equal(t, "main", out.Branch)
	assert.Equal(t, 2, out.MessageCount, "1 user + 1 assistant")

	byRole := resultsByRole(out.Results)
	require.Len(t, byRole[roleUser], 1)
	assert.Equal(t, "Read the config and fix the timeout", byRole[roleUser][0].ContentText)

	require.Len(t, byRole[roleAssistant], 1)
	a := byRole[roleAssistant][0].ContentText
	assert.Contains(t, a, "Let me read the config first.")
	assert.Contains(t, a, "Read /proj/config.toml", "tool name + file path searchable")
	assert.Contains(t, a, "Bash go test ./...", "tool name + command searchable")
	assert.NotContains(t, a, "secret reasoning", "thinking blocks are skipped")
}

func TestScanSession_ToolResultFromUserAsRoleTool(t *testing.T) {
	// tool_result blocks live in *user* entries — they must be extracted as
	// Role="tool", never Role="user".
	r := buildJSONL(t,
		userLine("u1", "/p", "main", "Run the tests"),
		assistantLine("a1", "msg_1", []map[string]any{
			{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "go test"}},
		}),
		map[string]any{
			"type": "user", "uuid": "u2", "timestamp": "2026-05-01T10:00:06Z",
			"message": map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "t1", "content": []map[string]any{
					{"type": "text", "text": "PASS: all tests green"},
				}},
			}},
		},
	)

	out, err := ScanSession(r)
	require.NoError(t, err)

	byRole := resultsByRole(out.Results)
	require.Len(t, byRole[roleTool], 1)
	assert.Equal(t, "PASS: all tests green", byRole[roleTool][0].ContentText)

	// The tool_result-only user entry must NOT produce a Role="user" row, and
	// must NOT inflate MessageCount (it's tool output, not a turn).
	assert.Len(t, byRole[roleUser], 1, "only the real human prompt is Role=user")
	assert.Equal(t, 2, out.MessageCount, "tool_result-only user excluded from count")

	// A --role user search would therefore never surface the tool output.
	for _, res := range byRole[roleUser] {
		assert.NotContains(t, res.ContentText, "PASS")
	}
}

func TestScanSession_UserEntryWithBothTextAndToolResult(t *testing.T) {
	// A single user content array can carry both a tool_result (previous turn's
	// output) and human text. The text → Role=user, the tool_result → Role=tool,
	// both anchored to the same source line.
	r := buildJSONL(t, map[string]any{
		"type": "user", "uuid": "u1", "timestamp": "2026-05-01T10:00:00Z", "cwd": "/p", "gitBranch": "main",
		"message": map[string]any{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "t0", "content": []map[string]any{
				{"type": "text", "text": "previous build output"},
			}},
			{"type": "text", "text": "Given that, refactor the loop"},
		}},
	})

	out, err := ScanSession(r)
	require.NoError(t, err)

	byRole := resultsByRole(out.Results)
	require.Len(t, byRole[roleUser], 1)
	require.Len(t, byRole[roleTool], 1)
	assert.Equal(t, "Given that, refactor the loop", byRole[roleUser][0].ContentText)
	assert.Equal(t, "previous build output", byRole[roleTool][0].ContentText)
	assert.Equal(t, 0, byRole[roleUser][0].LineIndex)
	assert.Equal(t, 0, byRole[roleTool][0].LineIndex, "tool row shares the user entry's line")
	assert.Equal(t, 1, out.MessageCount, "the human turn counts; the tool_result does not")
}

func TestScanSession_ToolResultStringContentAndTruncation(t *testing.T) {
	// tool_result content can be a plain string, and long output is bounded
	// 75% head / 25% tail.
	long := strings.Repeat("H", 20000) + "MIDDLE" + strings.Repeat("T", 20000)
	r := buildJSONL(t,
		userLine("u1", "/p", "main", "go"),
		map[string]any{
			"type": "user", "uuid": "u2", "timestamp": "2026-05-01T10:00:06Z",
			"message": map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "t1", "content": long},
			}},
		},
	)

	out, err := ScanSession(r)
	require.NoError(t, err)

	byRole := resultsByRole(out.Results)
	require.Len(t, byRole[roleTool], 1)
	got := byRole[roleTool][0].ContentText
	gotRunes := []rune(got)
	assert.LessOrEqual(t, len(gotRunes), maxToolResultChars+1, "bounded to cap (+1 ellipsis)")
	assert.True(t, strings.HasPrefix(got, "H"), "keeps the head")
	assert.True(t, strings.HasSuffix(got, "T"), "keeps the tail")
	assert.Contains(t, got, "…", "head/tail joined by ellipsis")
}

func TestScanSession_ImageToolResultSkipped(t *testing.T) {
	r := buildJSONL(t,
		userLine("u1", "/p", "main", "screenshot"),
		map[string]any{
			"type": "user", "uuid": "u2", "timestamp": "2026-05-01T10:00:06Z",
			"message": map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "t1", "content": []map[string]any{
					{"type": "image", "source": map[string]any{"data": strings.Repeat("A", 500)}},
				}},
			}},
		},
	)

	out, err := ScanSession(r)
	require.NoError(t, err)
	assert.Empty(t, resultsByRole(out.Results)[roleTool], "image-only tool_result yields no row")
}

func TestScanSession_AITitleLastWins(t *testing.T) {
	r := buildJSONL(t,
		map[string]any{"type": "ai-title", "aiTitle": "First draft title", "sessionId": "s"},
		userLine("u1", "/p", "main", "hello"),
		map[string]any{"type": "ai-title", "aiTitle": "Refined final title", "sessionId": "s"},
	)

	out, err := ScanSession(r)
	require.NoError(t, err)
	assert.Equal(t, "Refined final title", out.Title)
}

func TestScanSession_TitleFallbackSkipsToolResultAndTags(t *testing.T) {
	// No ai-title → fall back to the first *significant* user message: not a
	// tool_result array, not a <…>-prefixed tag.
	r := buildJSONL(t,
		map[string]any{ // tool_result-only user: skipped for title
			"type": "user", "uuid": "u1", "timestamp": "2026-05-01T10:00:00Z",
			"message": map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "t0", "content": "stale output"},
			}},
		},
		userLine("u2", "/p", "main", "<command-name>/compact</command-name>"), // tag: skipped
		userLine("u3", "/p", "main", "How do I configure retries?"),           // first real message
		assistantLine("a1", "msg_1", []map[string]any{{"type": "text", "text": "Use the retry option."}}),
	)

	out, err := ScanSession(r)
	require.NoError(t, err)
	assert.Equal(t, "How do I configure retries?", out.Title)
}

func TestScanSession_TitleFallbackTruncatedTo120(t *testing.T) {
	longMsg := strings.Repeat("x", 200)
	r := buildJSONL(t, userLine("u1", "/p", "main", longMsg))

	out, err := ScanSession(r)
	require.NoError(t, err)
	assert.Equal(t, []rune(longMsg)[:titleMaxChars], []rune(strings.TrimSuffix(out.Title, "…")))
	assert.True(t, strings.HasSuffix(out.Title, "…"))
}

func TestScanSession_SystemReminderStripped(t *testing.T) {
	r := buildJSONL(t,
		userLine("u1", "/p", "main", "Fix the bug<system-reminder>hook noise here</system-reminder> in parser"),
	)
	out, err := ScanSession(r)
	require.NoError(t, err)
	require.Len(t, out.Results, 1)
	assert.Equal(t, "Fix the bug in parser", out.Results[0].ContentText)
	assert.NotContains(t, out.Results[0].ContentText, "hook noise")
}

func TestScanSession_ProgressiveSnapshotsDeduped(t *testing.T) {
	// Same message.id across snapshots; LineIndex must point at the first
	// (canonical) snapshot and text must not duplicate.
	r := buildJSONL(t,
		userLine("u1", "/p", "main", "Explain the design"),
		assistantLine("a1-snap1", "msg_same", []map[string]any{
			{"type": "thinking", "thinking": "", "signature": "sig"},
		}),
		assistantLine("a1-snap2", "msg_same", []map[string]any{
			{"type": "thinking", "thinking": "", "signature": "sig"},
			{"type": "text", "text": "It uses a layered architecture."},
		}),
	)

	out, err := ScanSession(r)
	require.NoError(t, err)

	byRole := resultsByRole(out.Results)
	require.Len(t, byRole[roleAssistant], 1, "snapshots collapse to one message")
	assert.Equal(t, "It uses a layered architecture.", byRole[roleAssistant][0].ContentText)
	assert.Equal(t, 1, byRole[roleAssistant][0].LineIndex, "anchor is the first snapshot line")
}

func TestScanSession_LineIndexTracksSourceLine(t *testing.T) {
	r := buildJSONL(t,
		map[string]any{"type": "permission-mode", "sessionId": "s"}, // line 0 (skipped)
		userLine("u1", "/p", "main", "first question"),              // line 1
		assistantLine("a1", "msg_1", []map[string]any{{"type": "text", "text": "answer"}}), // line 2
		userLine("u2", "/p", "main", "second question"),             // line 3
	)

	out, err := ScanSession(r)
	require.NoError(t, err)

	byRole := resultsByRole(out.Results)
	require.Len(t, byRole[roleUser], 2)
	assert.Equal(t, 1, byRole[roleUser][0].LineIndex)
	assert.Equal(t, 3, byRole[roleUser][1].LineIndex)
	require.Len(t, byRole[roleAssistant], 1)
	assert.Equal(t, 2, byRole[roleAssistant][0].LineIndex)
}

func TestScanSession_TurnAndMessageIndex(t *testing.T) {
	r := buildJSONL(t,
		userLine("u1", "/p", "main", "q1"),
		assistantLine("a1", "msg_1", []map[string]any{{"type": "text", "text": "a1"}}),
		userLine("u2", "/p", "main", "q2"),
		assistantLine("a2", "msg_2", []map[string]any{{"type": "text", "text": "a2"}}),
	)

	out, err := ScanSession(r)
	require.NoError(t, err)
	require.Len(t, out.Results, 4)

	assert.Equal(t, 0, out.Results[0].TurnIndex) // q1
	assert.Equal(t, 0, out.Results[0].MessageIndex)
	assert.Equal(t, 0, out.Results[1].TurnIndex) // a1
	assert.Equal(t, 1, out.Results[1].MessageIndex)
	assert.Equal(t, 1, out.Results[2].TurnIndex) // q2 starts a new turn
	assert.Equal(t, 0, out.Results[2].MessageIndex)
	assert.Equal(t, 1, out.Results[3].TurnIndex) // a2
	assert.Equal(t, 1, out.Results[3].MessageIndex)
}

func TestScanSession_PRLinkExtracted(t *testing.T) {
	r := buildJSONL(t,
		userLine("u1", "/p", "main", "open a PR"),
		map[string]any{
			"type": "pr-link", "sessionId": "s", "timestamp": "2026-05-01T10:01:00Z",
			"prUrl": "https://github.com/acme/widgets/pull/42", "prRepository": "acme/widgets", "prNumber": 42,
		},
	)

	out, err := ScanSession(r)
	require.NoError(t, err)

	byRole := resultsByRole(out.Results)
	require.Len(t, byRole[roleSystem], 1)
	pr := byRole[roleSystem][0].ContentText
	assert.Contains(t, pr, "PR #42")
	assert.Contains(t, pr, "acme/widgets")
	assert.Contains(t, pr, "https://github.com/acme/widgets/pull/42")
}

func TestScanSession_AwaySummaryExtracted(t *testing.T) {
	r := buildJSONL(t,
		userLine("u1", "/p", "main", "start"),
		map[string]any{
			"type": "system", "subtype": "away_summary", "timestamp": "2026-05-01T11:00:00Z",
			"content": "We configured the dependencies and set up CI.",
		},
	)

	out, err := ScanSession(r)
	require.NoError(t, err)
	byRole := resultsByRole(out.Results)
	require.Len(t, byRole[roleSystem], 1)
	assert.Equal(t, "We configured the dependencies and set up CI.", byRole[roleSystem][0].ContentText)
}

func TestScanSession_UnindexedTypesProduceNothing(t *testing.T) {
	r := buildJSONL(t,
		map[string]any{"type": "agent-name", "agentName": "Explore", "sessionId": "s"},
		map[string]any{"type": "progress", "sessionId": "s"},
		map[string]any{"type": "custom-title", "customTitle": "My renamed session", "sessionId": "s"},
		map[string]any{"type": "permission-mode", "sessionId": "s"},
		map[string]any{"type": "file-history-snapshot", "sessionId": "s"},
		map[string]any{"type": "totally-unknown-future-type", "sessionId": "s"},
		map[string]any{"type": "system", "subtype": "turn_duration", "durationMs": 1234},
	)

	out, err := ScanSession(r)
	require.NoError(t, err)
	assert.Empty(t, out.Results, "no indexable types present")
	assert.Equal(t, 0, out.MessageCount)
	assert.Empty(t, out.Title, "custom-title is not sourced from the JSONL")
}

func TestScanSession_SecretsStripped(t *testing.T) {
	r := buildJSONL(t,
		userLine("u1", "/p", "main", "my key is sk-ant-abcdefghijklmnopqrstuvwxyz0123 keep it safe"),
	)
	out, err := ScanSession(r)
	require.NoError(t, err)
	require.Len(t, out.Results, 1)
	assert.NotContains(t, out.Results[0].ContentText, "sk-ant-abcdefghijklmnopqrstuvwxyz0123")
	assert.Contains(t, out.Results[0].ContentText, "[REDACTED_SECRET]")
}

func TestToolUseSummary(t *testing.T) {
	longPrompt := strings.Repeat("p", 300)
	tests := []struct {
		name  string
		tool  string
		input map[string]any
		want  string
	}{
		{"read file_path", "Read", map[string]any{"file_path": "/a/b.go"}, "Read /a/b.go"},
		{"write file_path", "Write", map[string]any{"file_path": "/a/c.go"}, "Write /a/c.go"},
		{"edit file_path", "Edit", map[string]any{"file_path": "/a/d.go"}, "Edit /a/d.go"},
		{"bash command", "Bash", map[string]any{"command": "ls -la"}, "Bash ls -la"},
		{"agent prompt", "Agent", map[string]any{"prompt": "explore the repo"}, "Agent explore the repo"},
		{"task prompt", "Task", map[string]any{"prompt": "find bugs"}, "Agent find bugs"},
		{"unknown tool → name only", "Glob", map[string]any{"pattern": "*.go"}, "Glob"},
		{"read missing file_path → name only", "Read", map[string]any{}, "Read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, err := json.Marshal(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, toolUseSummary(tt.tool, input))
		})
	}

	t.Run("agent prompt truncated to cap", func(t *testing.T) {
		input, err := json.Marshal(map[string]any{"prompt": longPrompt})
		require.NoError(t, err)
		got := toolUseSummary("Agent", input)
		assert.True(t, strings.HasPrefix(got, "Agent "))
		assert.True(t, strings.HasSuffix(got, "…"))
		assert.Len(t, []rune(strings.TrimSuffix(strings.TrimPrefix(got, "Agent "), "…")), agentPromptMaxChars)
	})
}

func TestScanSession_AttachmentBestEffort(t *testing.T) {
	// The attachment schema is unverified, so extraction is best-effort: pull
	// filename-ish fields from content blocks (or a plain-string content), and
	// produce nothing when no recognized field is present.
	t.Run("filename from content block", func(t *testing.T) {
		r := buildJSONL(t, map[string]any{
			"type": "attachment", "uuid": "at1", "timestamp": "2026-05-01T10:00:00Z",
			"message": map[string]any{"role": "user", "content": []map[string]any{
				{"type": "image", "filename": "architecture-diagram.png"},
			}},
		})
		out, err := ScanSession(r)
		require.NoError(t, err)
		require.Len(t, out.Results, 1)
		assert.Equal(t, roleSystem, out.Results[0].Role)
		assert.Contains(t, out.Results[0].ContentText, "architecture-diagram.png")
	})

	t.Run("plain string content", func(t *testing.T) {
		r := buildJSONL(t, map[string]any{
			"type": "attachment", "uuid": "at2", "timestamp": "2026-05-01T10:00:00Z",
			"message": map[string]any{"role": "user", "content": "notes.txt"},
		})
		out, err := ScanSession(r)
		require.NoError(t, err)
		require.Len(t, out.Results, 1)
		assert.Equal(t, "notes.txt", out.Results[0].ContentText)
	})

	t.Run("no recognized field → no result", func(t *testing.T) {
		r := buildJSONL(t, map[string]any{
			"type": "attachment", "uuid": "at3", "timestamp": "2026-05-01T10:00:00Z",
			"message": map[string]any{"role": "user", "content": []map[string]any{
				{"type": "image", "mediaType": "image/png", "bytes": 1024},
			}},
		})
		out, err := ScanSession(r)
		require.NoError(t, err)
		assert.Empty(t, out.Results, "attachment with no filename-ish field is skipped")
	})
}

func TestScanSession_TitleSanitized(t *testing.T) {
	// The title is surfaced by `capy vault list`, so secrets in either source
	// (ai-title or the first-user-message fallback) must be redacted.
	t.Run("ai-title", func(t *testing.T) {
		r := buildJSONL(t,
			map[string]any{"type": "ai-title", "aiTitle": "Deploy with sk-ant-abcdefghijklmnopqrstuvwxyz0123", "sessionId": "s"},
			userLine("u1", "/p", "main", "go"),
		)
		out, err := ScanSession(r)
		require.NoError(t, err)
		assert.NotContains(t, out.Title, "sk-ant-abcdefghijklmnopqrstuvwxyz0123")
		assert.Contains(t, out.Title, "[REDACTED_SECRET]")
	})

	t.Run("first-message fallback", func(t *testing.T) {
		r := buildJSONL(t, userLine("u1", "/p", "main", "use key sk-ant-abcdefghijklmnopqrstuvwxyz0123 to deploy"))
		out, err := ScanSession(r)
		require.NoError(t, err)
		assert.NotContains(t, out.Title, "sk-ant-abcdefghijklmnopqrstuvwxyz0123")
		assert.Contains(t, out.Title, "[REDACTED_SECRET]")
	})
}

func TestScanSubagent_SetsSubagentID(t *testing.T) {
	r := buildJSONL(t,
		map[string]any{
			"type": "user", "uuid": "su1", "timestamp": "2026-05-01T10:00:00Z", "isSidechain": true,
			"message": map[string]any{"role": "user", "content": "Explore the API endpoints"},
		},
		map[string]any{
			"type": "assistant", "uuid": "sa1", "timestamp": "2026-05-01T10:00:10Z", "isSidechain": true,
			"message": map[string]any{"id": "m1", "role": "assistant", "content": []map[string]any{
				{"type": "text", "text": "Found 5 endpoints in server.go"},
			}},
		},
	)

	results, err := ScanSubagent(r, "agent-abc123")
	require.NoError(t, err)
	require.NotEmpty(t, results)
	for _, res := range results {
		assert.Equal(t, "agent-abc123", res.SubagentID)
	}
}

func TestScanSession_MalformedLinesSkipped(t *testing.T) {
	body := strings.Join([]string{
		"not json at all",
		`{"type":"user","uuid":"u1","timestamp":"2026-05-01T10:00:00Z","message":{"role":"user","content":"real message"}}`,
		"{truncated",
		`{"type":"assistant","uuid":"a1","timestamp":"2026-05-01T10:00:05Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"a reply"}]}}`,
	}, "\n") + "\n"

	out, err := ScanSession(strings.NewReader(body))
	require.NoError(t, err)
	byRole := resultsByRole(out.Results)
	require.Len(t, byRole[roleUser], 1)
	require.Len(t, byRole[roleAssistant], 1)
	// LineIndex still tracks physical lines despite the skipped malformed ones.
	assert.Equal(t, 1, byRole[roleUser][0].LineIndex)
	assert.Equal(t, 3, byRole[roleAssistant][0].LineIndex)
}

func TestScanSession_EmptyInput(t *testing.T) {
	out, err := ScanSession(strings.NewReader(""))
	require.NoError(t, err)
	assert.Empty(t, out.Results)
	assert.Equal(t, 0, out.MessageCount)
	assert.Empty(t, out.Title)
}

func TestScanSession_TypeInferredFromMessageRole(t *testing.T) {
	// A line missing the top-level "type" still routes via message.role.
	r := buildJSONL(t,
		map[string]any{
			"uuid": "u1", "timestamp": "2026-05-01T10:00:00Z", "cwd": "/p", "gitBranch": "main",
			"message": map[string]any{"role": "user", "content": "inferred user line"},
		},
		map[string]any{
			"uuid": "a1", "timestamp": "2026-05-01T10:00:05Z",
			"message": map[string]any{"id": "m1", "role": "assistant", "content": []map[string]any{
				{"type": "text", "text": "inferred assistant line"},
			}},
		},
	)

	out, err := ScanSession(r)
	require.NoError(t, err)
	assert.Equal(t, 2, out.MessageCount)
}

// --- scanLines (oversize handling) ---

func collectLines(t *testing.T, body string, maxBytes int) (lines []string, oversize []int) {
	t.Helper()
	idx := -1
	err := scanLines(strings.NewReader(body), maxBytes, func(data []byte, over bool) {
		idx++
		if over {
			oversize = append(oversize, idx)
			lines = append(lines, "")
			return
		}
		lines = append(lines, string(data))
	})
	require.NoError(t, err)
	return lines, oversize
}

func TestScanLines_SkipsOversizeAndKeepsLineIndex(t *testing.T) {
	huge := strings.Repeat("x", 500)
	body := "first\n" + huge + "\nthird\n"

	lines, oversize := collectLines(t, body, 100)

	require.Len(t, lines, 3, "every physical line advances the callback exactly once")
	assert.Equal(t, "first", lines[0])
	assert.Equal(t, "", lines[1], "oversize line is dropped")
	assert.Equal(t, "third", lines[2])
	assert.Equal(t, []int{1}, oversize, "the middle line is reported oversize at index 1")
}

func TestScanLines_NoTrailingNewline(t *testing.T) {
	lines, oversize := collectLines(t, "a\nb", 1024)
	assert.Equal(t, []string{"a", "b"}, lines)
	assert.Empty(t, oversize)
}

func TestScanLines_CRLFLineEndings(t *testing.T) {
	lines, oversize := collectLines(t, "a\r\nb\r\n", 1024)
	assert.Equal(t, []string{"a", "b"}, lines, "trimEOL strips the trailing \\r")
	assert.Empty(t, oversize)
}

func TestScanLines_LongLineWithinCapAccumulates(t *testing.T) {
	// Longer than the 64KB internal buffer is fine as long as it's under the cap.
	long := strings.Repeat("y", 200*1024)
	lines, oversize := collectLines(t, "a\n"+long+"\nb\n", 1024*1024)
	require.Len(t, lines, 3)
	assert.Equal(t, long, lines[1])
	assert.Empty(t, oversize)
}
