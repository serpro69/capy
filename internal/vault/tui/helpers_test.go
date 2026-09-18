package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/serpro69/capy/internal/vault"
)

// displayWidth is the ANSI-aware terminal width of the widest line in s — what
// the terminal actually consumes, as opposed to len() or a rune count.
func displayWidth(s string) int { return lipgloss.Width(s) }

// firstLine returns the first row of a rendered view.
func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

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
		userLine("first question about timeouts"),                                                 // line 0
		assistantLine("m1", []map[string]any{textBlock("answer one"), taskBlock("explore code")}), // line 1
		userLine("second question"),                                                               // line 2
		assistantLine("m2", []map[string]any{textBlock("final answer")}),                          // line 3
	)
	sub := jsonlLines(t,
		userLine("subagent prompt"), // line 0
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

// --- Codex fixtures -----------------------------------------------------------
//
// Minimal legacy-mode (CLI < 0.147) rollout lines, the subset of
// internal/vault's codex_fixtures_test.go builders the TUI needs: a parent that
// spawns a child (an openable marker carrying ChildUUID) and the child itself
// (a standalone session with parent_uuid). Real UUIDv7 shapes, so shortID's
// 12-character cut is meaningful.

const (
	codexParentID     = "019dc606-f552-7f93-b9a1-c5620b23b8dd"
	codexChildID      = "019dc608-0061-7d90-a4e9-fca142504625"
	codexGrandchildID = "01a0714f-f6ea-7bb1-bdeb-dc80fab2f1bf"
	codexTS           = "2026-05-01T10:00:00.000Z"
)

func codexEnv(typ string, payload map[string]any) map[string]any {
	return map[string]any{"timestamp": codexTS, "type": typ, "payload": payload}
}

// codexSessionMeta is line 0 of a rollout; a non-empty parent makes it a
// sub-agent rollout (source.subagent.thread_spawn.parent_thread_id).
func codexSessionMeta(id, parent string) map[string]any {
	p := map[string]any{"id": id, "cwd": "/home/u/codexproj", "cli_version": "0.130.0", "timestamp": codexTS}
	if parent == "" {
		p["source"] = "cli"
	} else {
		p["source"] = map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": parent, "depth": 1}}}
		p["agent_nickname"] = "Boole"
		p["agent_role"] = "code-reviewer"
	}
	return codexEnv("session_meta", p)
}

func codexHumanLine(text string) map[string]any {
	return codexEnv("event_msg", map[string]any{"type": "user_message", "message": text})
}

func codexAssistantLine(text string) map[string]any {
	return codexEnv("response_item", map[string]any{"type": "message", "role": "assistant",
		"content": []map[string]any{{"type": "output_text", "text": text}}})
}

// codexSpawnLines is a resolved spawn_agent launch: the call, the parent-side
// collab_agent_spawn_end naming the child thread, and the call's output.
func codexSpawnLines(callID, childID string) []map[string]any {
	return []map[string]any{
		codexEnv("response_item", map[string]any{"type": "function_call", "name": "spawn_agent", "call_id": callID,
			"arguments": `{"task_name":"review","agent_type":"code-reviewer"}`}),
		codexEnv("event_msg", map[string]any{"type": "collab_agent_spawn_end", "call_id": callID, "new_thread_id": childID}),
		codexEnv("response_item", map[string]any{"type": "function_call_output", "call_id": callID, "output": `{"agent_id":"` + childID + `"}`}),
	}
}

// codexSession builds a Codex vault.Session from rollout lines.
func codexSession(t *testing.T, id, parent, title string, lines ...map[string]any) vault.Session {
	t.Helper()
	all := append([]map[string]any{codexSessionMeta(id, parent)}, lines...)
	return vault.Session{
		UUID: id, Title: title, ParentUUID: parent, Platform: vault.PlatformCodex,
		ProjectPath: "/home/u/codexproj", RawJSONL: jsonlLines(t, all...),
	}
}

// codexParentSession is a parent rollout with filler assistant turns (so the
// transcript is taller than a short viewport and the marker sits off-screen)
// followed by one resolved spawn of childID.
func codexParentSession(t *testing.T, id, childID string, filler int) vault.Session {
	t.Helper()
	lines := []map[string]any{codexHumanLine("please review the latest commit")}
	for i := range filler {
		lines = append(lines, codexAssistantLine(fmt.Sprintf("parent body line %d", i)))
	}
	lines = append(lines, codexAssistantLine("spawning a reviewer"))
	lines = append(lines, codexSpawnLines("call_"+childID[:8], childID)...)
	return codexSession(t, id, "", "Parent review", lines...)
}

// codexChildSession is a leaf sub-agent rollout: no human turn (CLI ≥ 0.147
// shape), one assistant entry.
func codexChildSession(t *testing.T, id, parent string) vault.Session {
	t.Helper()
	return codexSession(t, id, parent, "Boole · code-reviewer", codexAssistantLine("child findings about the commit"))
}

// stubStore is an in-memory dataStore (and searcher) for driving the models
// without an encrypted DB.
type stubStore struct {
	sessions  []vault.Session
	files     map[string][]vault.File
	results   []vault.SearchResult
	searchErr error
	renameErr error
	listErr   error // returned by ListSessions when set (post-rename refresh failure)
	getErr    error // returned by GetSession when set (child-open store failure)
	filesErr  error // returned by GetFiles when set

	searchCalls    int
	lastQuery      string
	lastSearchOpts vault.SearchOptions
	listCalls      int
	lastListOpts   vault.ListOptions
	renameCalls    int
	lastRenameID   string
	lastRenameOpts vault.RenameOptions
}

func (s *stubStore) ListSessions(_ context.Context, opts vault.ListOptions) ([]vault.Session, error) {
	s.listCalls++
	s.lastListOpts = opts
	if s.listErr != nil {
		return nil, s.listErr
	}
	// Mirror the real store's predicates so list tests exercise the same
	// narrowing the production query performs: the substring match on
	// project_path, and the default `parent_uuid IS NULL` that hides child
	// sessions unless IncludeChildren is set.
	var out []vault.Session
	for _, sess := range s.sessions {
		if opts.Project != "" && !strings.Contains(sess.ProjectPath, opts.Project) {
			continue
		}
		if opts.Platform != "" && sess.Platform.OrClaude() != opts.Platform {
			continue
		}
		if !opts.IncludeChildren && sess.ParentUUID != "" {
			continue
		}
		out = append(out, sess)
	}
	return out, nil
}

func (s *stubStore) GetSession(_ context.Context, prefix string) (*vault.Session, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	for i := range s.sessions {
		if strings.HasPrefix(s.sessions[i].UUID, prefix) {
			cp := s.sessions[i]
			return &cp, nil
		}
	}
	return nil, vault.ErrSessionNotFound
}

func (s *stubStore) GetFiles(_ context.Context, uuid string) ([]vault.File, error) {
	if s.filesErr != nil {
		return nil, s.filesErr
	}
	return s.files[uuid], nil
}

func (s *stubStore) Search(_ context.Context, opts vault.SearchOptions) ([]vault.SearchResult, error) {
	s.searchCalls++
	s.lastQuery = opts.Query
	s.lastSearchOpts = opts
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	var out []vault.SearchResult
	for _, result := range s.results {
		if opts.Platform != "" && result.Platform.OrClaude() != opts.Platform {
			continue
		}
		out = append(out, result)
	}
	return out, nil
}

// RenameSession mirrors the real store's contract: shared normalization for a
// rename, a nil CustomTitle tombstone for a clear, and the updated metadata
// returned — so app-level tests observe authoritative post-write state.
func (s *stubStore) RenameSession(_ context.Context, prefix string, opts vault.RenameOptions) (*vault.Session, error) {
	s.renameCalls++
	s.lastRenameID = prefix
	s.lastRenameOpts = opts
	if s.renameErr != nil {
		return nil, s.renameErr
	}
	for i := range s.sessions {
		if !strings.HasPrefix(s.sessions[i].UUID, prefix) {
			continue
		}
		var custom *string
		if !opts.Clear {
			normalized, err := vault.NormalizeSessionName(opts.Name)
			if err != nil {
				return nil, err
			}
			custom = &normalized
		}
		s.sessions[i].Name = &vault.SessionName{CustomTitle: custom, RenamedAtNS: int64(s.renameCalls), MachineID: "stub"}
		cp := s.sessions[i]
		return &cp, nil
	}
	return nil, vault.ErrSessionNotFound
}
