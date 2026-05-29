package vault

import "encoding/json"

// scanner_types.go holds the minimal JSON wire types the vault scanner needs to
// pull searchable text out of Claude Code session JSONL. They are intentionally
// decoupled from internal/session/parse.go: that parser is lossy by design
// (it filters tool results and builds turn pairs for BM25 chunking), whereas the
// vault scanner indexes broadly (tool results included, one FTS row per message)
// and must not inherit those operational decisions.

// jsonlLine is the top-level structure of each JSONL entry. Fields beyond the
// session/parse.go set carry vault-specific signals: cwd/gitBranch (location),
// aiTitle (session title), prUrl/prRepository/prNumber (pr-link search). There is
// deliberately no customTitle field — that title tier was absent from the
// 223-session sample and is deferred (see design.md title rationale).
type jsonlLine struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype"`
	UUID         string          `json:"uuid"`
	Timestamp    string          `json:"timestamp"`
	SessionID    string          `json:"sessionId"`
	CWD          string          `json:"cwd"`
	GitBranch    string          `json:"gitBranch"`
	AITitle      string          `json:"aiTitle"`
	PRURL        string          `json:"prUrl"`
	PRRepository string          `json:"prRepository"`
	PRNumber     json.Number     `json:"prNumber"`
	Content      string          `json:"content"` // top-level string (system away_summary)
	Message      json.RawMessage `json:"message"`
}

// jsonlMessage is the nested message object within user/assistant entries.
type jsonlMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// contentBlock represents a single block within message.content arrays. Input
// carries tool_use arguments; ToolUseID/Content carry the tool_result payload
// (Content is the nested result, which may itself be a string or a block array).
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}
