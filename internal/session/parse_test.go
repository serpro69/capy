package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// jsonlEntry is a helper for building test JSONL lines.
type jsonlEntry map[string]any

func writeJSONL(t *testing.T, dir, name string, lines []jsonlEntry) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, line := range lines {
		if err := enc.Encode(line); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestParseSession_ValidConversation(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONL(t, dir, "abc123.jsonl", []jsonlEntry{
		{
			"type":      "permission-mode",
			"sessionId": "abc123",
		},
		{
			"type":      "user",
			"uuid":      "u1",
			"timestamp": "2026-04-05T12:06:26Z",
			"sessionId": "abc123",
			"message": map[string]any{
				"role":    "user",
				"content": "How do I set up the project?",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a1",
			"timestamp": "2026-04-05T12:06:30Z",
			"sessionId": "abc123",
			"message": map[string]any{
				"id":   "msg_001",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "You can set up the project by running `make setup`. This will install all dependencies and configure the development environment."},
				},
			},
		},
		{
			"type":      "user",
			"uuid":      "u2",
			"timestamp": "2026-04-05T12:07:00Z",
			"sessionId": "abc123",
			"message": map[string]any{
				"role":    "user",
				"content": "What about testing?",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a2",
			"timestamp": "2026-04-05T12:07:05Z",
			"sessionId": "abc123",
			"message": map[string]any{
				"id":   "msg_002",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "Run `make test` to execute the full test suite. Use `-tags fts5` for SQLite FTS tests."},
				},
			},
		},
	})

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	if session.SessionID != "abc123" {
		t.Errorf("SessionID = %q, want %q", session.SessionID, "abc123")
	}

	wantTime, _ := time.Parse(time.RFC3339, "2026-04-05T12:06:26Z")
	if !session.StartTime.Equal(wantTime) {
		t.Errorf("StartTime = %v, want %v", session.StartTime, wantTime)
	}

	if len(session.TurnPairs) != 2 {
		t.Fatalf("got %d turn pairs, want 2", len(session.TurnPairs))
	}

	tp := session.TurnPairs[0]
	if tp.HumanText != "How do I set up the project?" {
		t.Errorf("turn 0 HumanText = %q", tp.HumanText)
	}
	if tp.AssistantText == "" {
		t.Error("turn 0 AssistantText is empty")
	}

	tp2 := session.TurnPairs[1]
	if tp2.HumanText != "What about testing?" {
		t.Errorf("turn 1 HumanText = %q", tp2.HumanText)
	}

	if session.TotalAssistantChars <= 0 {
		t.Errorf("TotalAssistantChars = %d, want > 0", session.TotalAssistantChars)
	}
}

func TestParseSession_EmptySession(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONL(t, dir, "empty.jsonl", []jsonlEntry{
		{"type": "permission-mode", "sessionId": "empty"},
		{"type": "attachment", "uuid": "att1"},
	})

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(session.TurnPairs) != 0 {
		t.Errorf("got %d turn pairs, want 0", len(session.TurnPairs))
	}
	if session.IsIndexable() {
		t.Error("empty session should not be indexable")
	}
}

func TestParseSession_ToolResultOnly(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONL(t, dir, "tools.jsonl", []jsonlEntry{
		{
			"type":      "user",
			"uuid":      "u1",
			"timestamp": "2026-04-05T12:00:00Z",
			"sessionId": "tools-session",
			"message": map[string]any{
				"role": "user",
				"content": []map[string]any{
					{"type": "tool_result", "tool_use_id": "toolu_123", "content": []map[string]any{
						{"type": "text", "text": "file contents here"},
					}},
				},
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a1",
			"timestamp": "2026-04-05T12:00:01Z",
			"sessionId": "tools-session",
			"message": map[string]any{
				"id":   "msg_001",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "I see the file contents."},
				},
			},
		},
	})

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(session.TurnPairs) != 0 {
		t.Errorf("tool-result-only user should not create a turn pair, got %d", len(session.TurnPairs))
	}
}

func TestParseSession_AwaySummary(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONL(t, dir, "resume.jsonl", []jsonlEntry{
		{
			"type":      "user",
			"uuid":      "u1",
			"timestamp": "2026-04-05T12:00:00Z",
			"sessionId": "resume-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Start the project",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a1",
			"timestamp": "2026-04-05T12:00:05Z",
			"sessionId": "resume-session",
			"message": map[string]any{
				"id":   "msg_001",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "Setting up the project now with all required dependencies and configuration files."},
				},
			},
		},
		{
			"type":      "system",
			"subtype":   "away_summary",
			"content":   "We set up the project and configured all dependencies.",
			"uuid":      "s1",
			"timestamp": "2026-04-05T13:00:00Z",
			"sessionId": "resume-session",
		},
		{
			"type":      "user",
			"uuid":      "u2",
			"timestamp": "2026-04-05T14:00:00Z",
			"sessionId": "resume-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Continue with the implementation of the feature we discussed yesterday",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a2",
			"timestamp": "2026-04-05T14:00:10Z",
			"sessionId": "resume-session",
			"message": map[string]any{
				"id":   "msg_002",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "Continuing with the implementation. Let me pick up where we left off with the feature."},
				},
			},
		},
	})

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(session.TurnPairs) != 3 {
		t.Fatalf("got %d turn pairs, want 3 (2 normal + 1 away_summary)", len(session.TurnPairs))
	}

	// The away_summary should appear as a turn pair with empty HumanText.
	summary := session.TurnPairs[1]
	if summary.HumanText != "" {
		t.Errorf("away_summary turn should have empty HumanText, got %q", summary.HumanText)
	}
	if summary.AssistantText != "We set up the project and configured all dependencies." {
		t.Errorf("away_summary text = %q", summary.AssistantText)
	}
}

func TestParseSession_SystemReminderStripping(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONL(t, dir, "reminder.jsonl", []jsonlEntry{
		{
			"type":      "user",
			"uuid":      "u1",
			"timestamp": "2026-04-05T12:00:00Z",
			"sessionId": "reminder-session",
			"message": map[string]any{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": "Fix the bug<system-reminder>Hook context here</system-reminder> in the parser"},
				},
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a1",
			"timestamp": "2026-04-05T12:00:05Z",
			"sessionId": "reminder-session",
			"message": map[string]any{
				"id":   "msg_001",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "I'll fix the parser bug now. This requires updating the regex and the test cases for completeness."},
				},
			},
		},
		{
			"type":      "user",
			"uuid":      "u2",
			"timestamp": "2026-04-05T12:01:00Z",
			"sessionId": "reminder-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Thanks, looks good",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a2",
			"timestamp": "2026-04-05T12:01:05Z",
			"sessionId": "reminder-session",
			"message": map[string]any{
				"id":   "msg_002",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "You're welcome! The fix is committed and all tests pass successfully now."},
				},
			},
		},
	})

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(session.TurnPairs) != 2 {
		t.Fatalf("got %d turn pairs, want 2", len(session.TurnPairs))
	}

	got := session.TurnPairs[0].HumanText
	if got != "Fix the bug in the parser" {
		t.Errorf("system-reminder not stripped: %q", got)
	}
}

func TestParseSession_ToolUseExtraction(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONL(t, dir, "tooluse.jsonl", []jsonlEntry{
		{
			"type":      "user",
			"uuid":      "u1",
			"timestamp": "2026-04-05T12:00:00Z",
			"sessionId": "tooluse-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Read the config file and fix the settings for the application",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a1",
			"timestamp": "2026-04-05T12:00:05Z",
			"sessionId": "tooluse-session",
			"message": map[string]any{
				"id":   "msg_001",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "Let me read the config file first to understand the current settings."},
					{"type": "tool_use", "id": "toolu_001", "name": "Read", "input": map[string]any{"path": "/config.toml"}},
				},
			},
		},
		// Tool result (user message with tool_result) — should be skipped.
		{
			"type":      "user",
			"uuid":      "u2",
			"timestamp": "2026-04-05T12:00:06Z",
			"sessionId": "tooluse-session",
			"message": map[string]any{
				"role": "user",
				"content": []map[string]any{
					{"type": "tool_result", "tool_use_id": "toolu_001", "content": []map[string]any{
						{"type": "text", "text": "[config contents]"},
					}},
				},
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a2",
			"timestamp": "2026-04-05T12:00:10Z",
			"sessionId": "tooluse-session",
			"message": map[string]any{
				"id":   "msg_002",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "I see the issue — the timeout is set too low. Let me fix it."},
					{"type": "tool_use", "id": "toolu_002", "name": "Edit", "input": map[string]any{"path": "/config.toml"}},
				},
			},
		},
		{
			"type":      "user",
			"uuid":      "u3",
			"timestamp": "2026-04-05T12:01:00Z",
			"sessionId": "tooluse-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Perfect, that fixed it",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a3",
			"timestamp": "2026-04-05T12:01:05Z",
			"sessionId": "tooluse-session",
			"message": map[string]any{
				"id":   "msg_003",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "Great! The config is updated and the timeout should work properly now."},
				},
			},
		},
	})

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	// Should have 2 turn pairs: "Read the config..." → assistant (with Read+Edit tools),
	// then "Perfect..." → assistant (no tools).
	// The tool_result-only user message is skipped, so assistant msgs with Read and Edit
	// both attach to the first turn pair.
	if len(session.TurnPairs) != 2 {
		t.Fatalf("got %d turn pairs, want 2", len(session.TurnPairs))
	}

	tp := session.TurnPairs[0]
	if len(tp.ToolNames) < 1 {
		t.Fatalf("turn 0 should have tool names, got %v", tp.ToolNames)
	}
	if tp.ToolNames[0] != "Read" {
		t.Errorf("turn 0 ToolNames[0] = %q, want %q", tp.ToolNames[0], "Read")
	}
}

func TestParseSession_ProgressiveSnapshots(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONL(t, dir, "snapshots.jsonl", []jsonlEntry{
		{
			"type":      "user",
			"uuid":      "u1",
			"timestamp": "2026-04-05T12:00:00Z",
			"sessionId": "snap-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Help me understand the code",
			},
		},
		// Progressive snapshot 1: thinking only
		{
			"type":      "assistant",
			"uuid":      "a1-snap1",
			"timestamp": "2026-04-05T12:00:01Z",
			"sessionId": "snap-session",
			"message": map[string]any{
				"id":   "msg_same",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "thinking", "thinking": "", "signature": "abc123"},
				},
			},
		},
		// Progressive snapshot 2: thinking + text (final)
		{
			"type":      "assistant",
			"uuid":      "a1-snap2",
			"timestamp": "2026-04-05T12:00:05Z",
			"sessionId": "snap-session",
			"message": map[string]any{
				"id":   "msg_same",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "thinking", "thinking": "", "signature": "abc123"},
					{"type": "text", "text": "The code follows a clean architecture pattern with clear separation of concerns between layers."},
				},
			},
		},
		{
			"type":      "user",
			"uuid":      "u2",
			"timestamp": "2026-04-05T12:01:00Z",
			"sessionId": "snap-session",
			"message": map[string]any{
				"role":    "user",
				"content": "That makes sense, thanks for explaining the architecture so clearly",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a2",
			"timestamp": "2026-04-05T12:01:05Z",
			"sessionId": "snap-session",
			"message": map[string]any{
				"id":   "msg_other",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "You're welcome! Let me know if you have any other questions about the codebase."},
				},
			},
		},
	})

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(session.TurnPairs) != 2 {
		t.Fatalf("got %d turn pairs, want 2", len(session.TurnPairs))
	}

	// Should NOT have duplicate text from progressive snapshots.
	tp := session.TurnPairs[0]
	if tp.AssistantText != "The code follows a clean architecture pattern with clear separation of concerns between layers." {
		t.Errorf("unexpected assistant text (possible dedup failure): %q", tp.AssistantText)
	}
}

func TestParseSession_SlashCommands(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONL(t, dir, "slash.jsonl", []jsonlEntry{
		{
			"type":      "user",
			"uuid":      "u1",
			"timestamp": "2026-04-05T12:00:00Z",
			"sessionId": "slash-session",
			"message": map[string]any{
				"role":    "user",
				"content": "<command-name>/effort</command-name>\n            <command-message>effort</command-message>\n            <command-args>max</command-args>",
			},
		},
		{
			"type":      "user",
			"uuid":      "u2",
			"timestamp": "2026-04-05T12:00:01Z",
			"sessionId": "slash-session",
			"message": map[string]any{
				"role":    "user",
				"content": "<local-command-stdout>Set effort level to max</local-command-stdout>",
			},
		},
		{
			"type":      "user",
			"uuid":      "u3",
			"timestamp": "2026-04-05T12:00:02Z",
			"sessionId": "slash-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Now fix the bug in parser.go",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a1",
			"timestamp": "2026-04-05T12:00:05Z",
			"sessionId": "slash-session",
			"message": map[string]any{
				"id":   "msg_001",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "I'll fix the parser bug. The issue is in the regex matching logic on line 42. I'll update it to handle edge cases properly."},
				},
			},
		},
		{
			"type":      "user",
			"uuid":      "u4",
			"timestamp": "2026-04-05T12:01:00Z",
			"sessionId": "slash-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Thanks for the fix",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a2",
			"timestamp": "2026-04-05T12:01:05Z",
			"sessionId": "slash-session",
			"message": map[string]any{
				"id":   "msg_002",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "You're welcome! The parser now handles all the edge cases correctly."},
				},
			},
		},
	})

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	// Slash command and stdout messages should be filtered out.
	// Only the real conversation should remain.
	if len(session.TurnPairs) != 2 {
		t.Fatalf("got %d turn pairs, want 2 (slash commands filtered)", len(session.TurnPairs))
	}

	if session.TurnPairs[0].HumanText != "Now fix the bug in parser.go" {
		t.Errorf("first human text = %q, want real message", session.TurnPairs[0].HumanText)
	}
}

func TestParseSession_MalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malformed.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("not json at all\n")
	f.WriteString(`{"type": "user", "uuid": "u1", "timestamp": "2026-04-05T12:00:00Z", "sessionId": "mal", "message": {"role": "user", "content": "Hello there"}}` + "\n")
	f.WriteString("{truncated\n")
	f.WriteString(`{"type": "assistant", "uuid": "a1", "timestamp": "2026-04-05T12:00:05Z", "sessionId": "mal", "message": {"id": "msg_001", "role": "assistant", "content": [{"type": "text", "text": "Hi! How can I help you with your code today? I am ready to assist."}]}}` + "\n")
	f.WriteString(`{"type": "user", "uuid": "u2", "timestamp": "2026-04-05T12:01:00Z", "sessionId": "mal", "message": {"role": "user", "content": "Fix the tests"}}` + "\n")
	f.WriteString(`{"type": "assistant", "uuid": "a2", "timestamp": "2026-04-05T12:01:05Z", "sessionId": "mal", "message": {"id": "msg_002", "role": "assistant", "content": [{"type": "text", "text": "I will fix the failing tests by updating the assertions to match the new behavior."}]}}` + "\n")
	f.Close()

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(session.TurnPairs) != 2 {
		t.Fatalf("got %d turn pairs, want 2 (malformed lines skipped)", len(session.TurnPairs))
	}
}

func TestParseSession_LocalCommandCaveat(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONL(t, dir, "caveat.jsonl", []jsonlEntry{
		{
			"type":      "user",
			"uuid":      "u1",
			"timestamp": "2026-04-05T12:00:00Z",
			"sessionId": "caveat-session",
			"message": map[string]any{
				"role":    "user",
				"content": "<local-command-caveat>Caveat: The messages below were generated by the user while running local commands.</local-command-caveat>",
			},
		},
		{
			"type":      "user",
			"uuid":      "u2",
			"timestamp": "2026-04-05T12:00:01Z",
			"sessionId": "caveat-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Implement the feature from the design doc we reviewed earlier today",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a1",
			"timestamp": "2026-04-05T12:00:05Z",
			"sessionId": "caveat-session",
			"message": map[string]any{
				"id":   "msg_001",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "I'll implement the feature now. Starting with the core data structures and then building out the API layer."},
				},
			},
		},
		{
			"type":      "user",
			"uuid":      "u3",
			"timestamp": "2026-04-05T12:01:00Z",
			"sessionId": "caveat-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Looks good",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a2",
			"timestamp": "2026-04-05T12:01:05Z",
			"sessionId": "caveat-session",
			"message": map[string]any{
				"id":   "msg_002",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "Great! All changes are committed and the tests are passing. The feature is ready for review."},
				},
			},
		},
	})

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(session.TurnPairs) != 2 {
		t.Fatalf("got %d turn pairs, want 2", len(session.TurnPairs))
	}

	if session.TurnPairs[0].HumanText != "Implement the feature from the design doc we reviewed earlier today" {
		t.Errorf("caveat not filtered, got: %q", session.TurnPairs[0].HumanText)
	}
}

func TestIsIndexable(t *testing.T) {
	tests := []struct {
		name    string
		session ParsedSession
		want    bool
	}{
		{
			name: "sufficient content",
			session: ParsedSession{
				TurnPairs: []TurnPair{
					{HumanText: "q1", AssistantText: strings.Repeat("a", 100)},
					{HumanText: "q2", AssistantText: strings.Repeat("b", 101)},
				},
				TotalAssistantChars: 201,
			},
			want: true,
		},
		{
			name: "one turn pair",
			session: ParsedSession{
				TurnPairs: []TurnPair{
					{HumanText: "q1", AssistantText: strings.Repeat("a", 300)},
				},
				TotalAssistantChars: 300,
			},
			want: false,
		},
		{
			name: "below char threshold",
			session: ParsedSession{
				TurnPairs: []TurnPair{
					{HumanText: "q1", AssistantText: "short"},
					{HumanText: "q2", AssistantText: "also short"},
				},
				TotalAssistantChars: 15,
			},
			want: false,
		},
		{
			name: "subagent pairs dont count toward minimum",
			session: ParsedSession{
				TurnPairs: []TurnPair{
					{HumanText: "q1", AssistantText: strings.Repeat("a", 200)},
					{HumanText: "q2", AssistantText: "sub", IsSubagent: true},
				},
				TotalAssistantChars: 203,
			},
			want: false,
		},
		{
			name: "empty session",
			session: ParsedSession{
				TurnPairs:          nil,
				TotalAssistantChars: 0,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.session.IsIndexable()
			if got != tt.want {
				t.Errorf("IsIndexable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseSubagents(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "abc123")
	subagentsDir := filepath.Join(sessionDir, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write agent JSONL.
	writeJSONL(t, subagentsDir, "agent-af9a804171ec.jsonl", []jsonlEntry{
		{
			"type":        "user",
			"uuid":        "su1",
			"timestamp":   "2026-04-05T12:00:00Z",
			"isSidechain": true,
			"message": map[string]any{
				"role":    "user",
				"content": "Explore the codebase to find all API endpoints and their handlers",
			},
		},
		{
			"type":        "assistant",
			"uuid":        "sa1",
			"timestamp":   "2026-04-05T12:00:10Z",
			"isSidechain": true,
			"message": map[string]any{
				"id":   "msg_sub_001",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "Found 5 API endpoints in the server package: health, search, index, cleanup, and stats."},
				},
			},
		},
		{
			"type":        "user",
			"uuid":        "su2",
			"timestamp":   "2026-04-05T12:01:00Z",
			"isSidechain": true,
			"message": map[string]any{
				"role":    "user",
				"content": "What about the MCP tool handlers?",
			},
		},
		{
			"type":        "assistant",
			"uuid":        "sa2",
			"timestamp":   "2026-04-05T12:01:10Z",
			"isSidechain": true,
			"message": map[string]any{
				"id":   "msg_sub_002",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "The MCP handlers are registered in server.go via registerTools(). Each tool is in its own file."},
				},
			},
		},
	})

	// Write meta.json.
	metaData, _ := json.Marshal(agentMeta{AgentType: "Explore", Description: "Explore API endpoints"})
	os.WriteFile(filepath.Join(subagentsDir, "agent-af9a804171ec.meta.json"), metaData, 0o644)

	pairs, err := ParseSubagents(sessionDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(pairs) != 2 {
		t.Fatalf("got %d sub-agent pairs, want 2", len(pairs))
	}

	for _, p := range pairs {
		if !p.IsSubagent {
			t.Error("sub-agent pair should have IsSubagent=true")
		}
		if p.SubagentType != "Explore" {
			t.Errorf("SubagentType = %q, want %q", p.SubagentType, "Explore")
		}
		if p.SubagentDesc != "Explore API endpoints" {
			t.Errorf("SubagentDesc = %q, want %q", p.SubagentDesc, "Explore API endpoints")
		}
	}
}

func TestParseSubagents_NoDirectory(t *testing.T) {
	dir := t.TempDir()
	pairs, err := ParseSubagents(filepath.Join(dir, "nonexistent"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 0 {
		t.Errorf("got %d pairs for nonexistent dir, want 0", len(pairs))
	}
}

func TestParseSession_ThinkingBlocksSkipped(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONL(t, dir, "thinking.jsonl", []jsonlEntry{
		{
			"type":      "user",
			"uuid":      "u1",
			"timestamp": "2026-04-05T12:00:00Z",
			"sessionId": "think-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Explain the architecture in detail",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a1",
			"timestamp": "2026-04-05T12:00:05Z",
			"sessionId": "think-session",
			"message": map[string]any{
				"id":   "msg_001",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "thinking", "thinking": "", "signature": "ErcJClkIDRgCKkC+...base64..."},
					{"type": "text", "text": "The architecture uses a layered approach with clear boundaries between the store, server, and config packages."},
				},
			},
		},
		{
			"type":      "user",
			"uuid":      "u2",
			"timestamp": "2026-04-05T12:01:00Z",
			"sessionId": "think-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Can you elaborate on the store layer and its responsibilities?",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a2",
			"timestamp": "2026-04-05T12:01:05Z",
			"sessionId": "think-session",
			"message": map[string]any{
				"id":   "msg_002",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "thinking", "thinking": "", "signature": "def456..."},
					{"type": "text", "text": "The store layer handles SQLite FTS5 indexing, search with BM25 ranking, and content lifecycle management through cleanup policies."},
				},
			},
		},
	})

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(session.TurnPairs) != 2 {
		t.Fatalf("got %d turn pairs, want 2", len(session.TurnPairs))
	}

	// Thinking content should NOT appear in assistant text.
	for i, tp := range session.TurnPairs {
		if strings.Contains(tp.AssistantText, "ErcJ") || strings.Contains(tp.AssistantText, "base64") {
			t.Errorf("turn %d contains thinking signature in text", i)
		}
	}
}

func TestParseSession_UserContentArrayWithMixedBlocks(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONL(t, dir, "mixed.jsonl", []jsonlEntry{
		{
			"type":      "user",
			"uuid":      "u1",
			"timestamp": "2026-04-05T12:00:00Z",
			"sessionId": "mixed-session",
			"message": map[string]any{
				"role": "user",
				"content": []map[string]any{
					{"type": "tool_result", "tool_use_id": "toolu_prev", "content": []map[string]any{
						{"type": "text", "text": "previous tool output here"},
					}},
					{"type": "text", "text": "Based on that output, can you refactor the function to improve performance?"},
				},
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a1",
			"timestamp": "2026-04-05T12:00:05Z",
			"sessionId": "mixed-session",
			"message": map[string]any{
				"id":   "msg_001",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "I'll refactor the function to use a more efficient algorithm that reduces the time complexity from O(n^2) to O(n log n)."},
				},
			},
		},
		{
			"type":      "user",
			"uuid":      "u2",
			"timestamp": "2026-04-05T12:01:00Z",
			"sessionId": "mixed-session",
			"message": map[string]any{
				"role":    "user",
				"content": "Good improvement",
			},
		},
		{
			"type":      "assistant",
			"uuid":      "a2",
			"timestamp": "2026-04-05T12:01:05Z",
			"sessionId": "mixed-session",
			"message": map[string]any{
				"id":   "msg_002",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "Thanks! The benchmarks confirm the performance improvement is significant in production workloads."},
				},
			},
		},
	})

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(session.TurnPairs) != 2 {
		t.Fatalf("got %d turn pairs, want 2", len(session.TurnPairs))
	}

	// text block should be kept even when tool_result is present.
	got := session.TurnPairs[0].HumanText
	if got != "Based on that output, can you refactor the function to improve performance?" {
		t.Errorf("mixed content text = %q", got)
	}
}

func TestParseSession_SessionIDFromFilename(t *testing.T) {
	dir := t.TempDir()
	path := writeJSONL(t, dir, "deadbeef-1234-5678-9abc-def012345678.jsonl", []jsonlEntry{
		{
			"type": "permission-mode",
		},
	})

	session, err := ParseSession(path)
	if err != nil {
		t.Fatal(err)
	}

	if session.SessionID != "deadbeef-1234-5678-9abc-def012345678" {
		t.Errorf("SessionID = %q, want UUID from filename", session.SessionID)
	}
}

