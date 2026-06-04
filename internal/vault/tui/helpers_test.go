package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/vault"
)

// jsonlLines renders maps as newline-delimited compact JSON — the on-disk shape
// of a session/subagent transcript.
func jsonlLines(t *testing.T, lines ...map[string]any) []byte {
	t.Helper()
	var sb strings.Builder
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatalf("marshal jsonl line: %v", err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}

func userLine(text string) map[string]any {
	return map[string]any{
		"type": "user", "timestamp": "2026-05-01T10:00:00Z",
		"cwd": "/p", "gitBranch": "main",
		"message": map[string]any{"role": "user", "content": text},
	}
}

func assistantLine(msgID string, blocks []map[string]any) map[string]any {
	return map[string]any{
		"type": "assistant", "timestamp": "2026-05-01T10:00:05Z",
		"message": map[string]any{"id": msgID, "role": "assistant", "content": blocks},
	}
}

func textBlock(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func taskBlock(desc string) map[string]any {
	return map[string]any{"type": "tool_use", "id": "t1", "name": "Task",
		"input": map[string]any{"description": desc, "prompt": "p", "subagent_type": "Explore"}}
}

// sampleSession builds a session whose main transcript launches one subagent, plus
// the matching subagents/agent-<id>.jsonl sidecar, so marker mapping and
// subagent-open paths can be exercised.
func sampleSession(t *testing.T) (vault.Session, []vault.File) {
	t.Helper()
	main := jsonlLines(t,
		userLine("first question about timeouts"),       // line 0
		assistantLine("m1", []map[string]any{textBlock("answer one"), taskBlock("explore code")}), // line 1
		userLine("second question"),                     // line 2
		assistantLine("m2", []map[string]any{textBlock("final answer")}), // line 3
	)
	sub := jsonlLines(t,
		userLine("subagent prompt"),                                  // line 0
		assistantLine("s1", []map[string]any{textBlock("subagent findings about the bug")}), // line 1
	)
	sess := vault.Session{
		UUID:        "abcdef0123456789",
		Title:       "Timeout investigation",
		ProjectPath: "/home/u/proj",
		EndTime:     time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		RawJSONL:    main,
	}
	files := []vault.File{
		{RelativePath: "subagents/agent-xyz.jsonl", RawContent: sub},
		{RelativePath: "tool-results/toolu_1.json", RawContent: []byte(`{"ignored":true}`)},
	}
	return sess, files
}

// stubStore is an in-memory dataStore (and searcher) for driving the models
// without an encrypted DB.
type stubStore struct {
	sessions []vault.Session
	files    map[string][]vault.File
	results  []vault.SearchResult
	searchErr error

	searchCalls int
	lastQuery   string
}

func (s *stubStore) ListSessions(vault.ListOptions) ([]vault.Session, error) {
	return s.sessions, nil
}

func (s *stubStore) GetSession(prefix string) (*vault.Session, error) {
	for i := range s.sessions {
		if strings.HasPrefix(s.sessions[i].UUID, prefix) {
			cp := s.sessions[i]
			return &cp, nil
		}
	}
	return nil, vault.ErrSessionNotFound
}

func (s *stubStore) GetFiles(uuid string) ([]vault.File, error) {
	return s.files[uuid], nil
}

func (s *stubStore) Search(opts vault.SearchOptions) ([]vault.SearchResult, error) {
	s.searchCalls++
	s.lastQuery = opts.Query
	return s.results, s.searchErr
}
