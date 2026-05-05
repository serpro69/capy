package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ParsedSession holds the filtered, structured content of a Claude Code session.
type ParsedSession struct {
	SessionID          string
	StartTime          time.Time
	TurnPairs          []TurnPair
	TotalAssistantChars int
}

// TurnPair is one human→assistant exchange, optionally with tool usage metadata.
type TurnPair struct {
	HumanText    string
	AssistantText string
	ToolNames    []string
	IsSubagent   bool
	SubagentType string
	SubagentDesc string
}

// IsIndexable returns true if the session has enough content to be worth indexing.
// Requires at least 2 non-subagent turn pairs and 200+ chars of assistant text.
func (s *ParsedSession) IsIndexable() bool {
	mainPairs := 0
	for _, tp := range s.TurnPairs {
		if !tp.IsSubagent {
			mainPairs++
		}
	}
	return mainPairs >= 2 && s.TotalAssistantChars >= 200
}

var (
	sysReminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	noiseTagRe    = regexp.MustCompile(`(?s)<(local-command-caveat|local-command-stdout|command-name|command-message|command-args)>.*?</(local-command-caveat|local-command-stdout|command-message|command-name|command-args)>`)
)

// jsonlLine is the top-level structure of each JSONL entry.
type jsonlLine struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	UUID      string          `json:"uuid"`
	Timestamp string          `json:"timestamp"`
	SessionID string          `json:"sessionId"`
	Content   string          `json:"content"`
	Message   json.RawMessage `json:"message"`
}

// jsonlMessage is the nested message object within user/assistant entries.
type jsonlMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// contentBlock represents a single block within message.content arrays.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

// parsedMessage is an intermediate representation after JSONL deduplication.
type parsedMessage struct {
	lineType  string // "user", "assistant", "away_summary"
	timestamp time.Time
	sessionID string
	text      string   // extracted human/assistant text
	toolNames []string // tool_use names (assistant only)
}

// ParseSession reads a Claude Code session JSONL file and returns structured
// content with noise filtered out. Malformed lines are logged and skipped.
func ParseSession(path string) (*ParsedSession, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening session file: %w", err)
	}
	defer f.Close()

	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")

	// First pass: parse all lines and deduplicate assistant progressive snapshots
	// by message.id (keep last occurrence which has the most complete content).
	type rawEntry struct {
		order   int
		line    jsonlLine
		msgID   string // message.id for assistant dedup; uuid for others
		rawMsg  jsonlMessage
		hasMsgP bool // whether Message field was present and parsed
	}

	var entries []rawEntry
	assistantLastIdx := make(map[string]int) // message.id → index in entries

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		data := scanner.Bytes()
		if len(data) == 0 {
			continue
		}

		var line jsonlLine
		if err := json.Unmarshal(data, &line); err != nil {
			slog.Warn("skipping malformed JSONL line", "file", path, "line", lineNum, "error", err)
			continue
		}

		entry := rawEntry{order: lineNum, line: line}

		if len(line.Message) > 0 && string(line.Message) != "null" {
			var msg jsonlMessage
			if err := json.Unmarshal(line.Message, &msg); err == nil {
				entry.rawMsg = msg
				entry.hasMsgP = true
				if line.Type == "" && msg.Role != "" {
					line.Type = msg.Role
					entry.line.Type = msg.Role
				}
			}
		}

		switch line.Type {
		case "user":
			entry.msgID = line.UUID
			entries = append(entries, entry)
		case "assistant":
			msgID := entry.rawMsg.ID
			if msgID == "" {
				msgID = line.UUID
			}
			entry.msgID = msgID
			if prevIdx, exists := assistantLastIdx[msgID]; exists {
				entries[prevIdx].order = -1 // mark superseded
			}
			assistantLastIdx[msgID] = len(entries)
			entries = append(entries, entry)
		case "system":
			if line.Subtype == "away_summary" {
				entry.msgID = line.UUID
				entries = append(entries, entry)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading session file: %w", err)
	}

	// Second pass: extract text from deduplicated entries and build messages.
	var messages []parsedMessage
	var startTime time.Time
	foundSessionID := sessionID

	for _, entry := range entries {
		if entry.order < 0 {
			continue // superseded progressive snapshot
		}

		ts, _ := time.Parse(time.RFC3339Nano, entry.line.Timestamp)
		if entry.line.SessionID != "" {
			foundSessionID = entry.line.SessionID
		}

		switch entry.line.Type {
		case "user":
			if !entry.hasMsgP {
				continue
			}
			text := extractUserText(entry.rawMsg.Content)
			if text == "" {
				continue
			}
			if startTime.IsZero() && !ts.IsZero() {
				startTime = ts
			}
			messages = append(messages, parsedMessage{
				lineType:  "user",
				timestamp: ts,
				sessionID: foundSessionID,
				text:      text,
			})

		case "assistant":
			if !entry.hasMsgP {
				continue
			}
			text, toolNames := extractAssistantContent(entry.rawMsg.Content)
			if text == "" && len(toolNames) == 0 {
				continue
			}
			messages = append(messages, parsedMessage{
				lineType:  "assistant",
				timestamp: ts,
				sessionID: foundSessionID,
				text:      text,
				toolNames: toolNames,
			})

		case "system":
			if entry.line.Subtype == "away_summary" && entry.line.Content != "" {
				messages = append(messages, parsedMessage{
					lineType:  "away_summary",
					timestamp: ts,
					sessionID: foundSessionID,
					text:      strings.TrimSpace(entry.line.Content),
				})
			}
		}
	}

	// Third pass: build turn pairs from sequential messages.
	turnPairs, totalChars := buildTurnPairs(messages)

	return &ParsedSession{
		SessionID:          foundSessionID,
		StartTime:          startTime,
		TurnPairs:          turnPairs,
		TotalAssistantChars: totalChars,
	}, nil
}

// extractUserText gets human text from a user message's content field.
// Content can be a JSON string or an array of content blocks.
// Returns empty string for tool-result-only messages, slash commands, and noise.
func extractUserText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try string first (most common for actual human input).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return cleanUserText(s)
	}

	// Array of content blocks.
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}

	var texts []string
	hasText := false
	for _, b := range blocks {
		if b.Type == "text" {
			hasText = true
			cleaned := cleanUserText(b.Text)
			if cleaned != "" {
				texts = append(texts, cleaned)
			}
		}
	}

	if !hasText {
		return "" // tool_result-only message
	}
	return strings.Join(texts, "\n")
}

// cleanUserText strips noise tags and checks for slash commands.
func cleanUserText(text string) string {
	text = sysReminderRe.ReplaceAllString(text, "")
	text = noiseTagRe.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "/") {
		return ""
	}
	return text
}

// extractAssistantContent extracts text and tool names from assistant content blocks.
func extractAssistantContent(raw json.RawMessage) (string, []string) {
	if len(raw) == 0 {
		return "", nil
	}

	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil
	}

	var texts []string
	var toolNames []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			t := strings.TrimSpace(b.Text)
			if t != "" {
				texts = append(texts, t)
			}
		case "tool_use":
			if b.Name != "" {
				toolNames = append(toolNames, b.Name)
			}
		}
		// thinking blocks: skip (content is empty/signature only)
	}

	return strings.Join(texts, "\n"), toolNames
}

// buildTurnPairs groups sequential user→assistant messages into pairs.
// Away summaries are emitted as standalone entries (empty HumanText).
func buildTurnPairs(messages []parsedMessage) ([]TurnPair, int) {
	var pairs []TurnPair
	totalChars := 0

	var pendingHuman *parsedMessage
	var currentAssistantText []string
	var currentToolNames []string

	flushPair := func() {
		if pendingHuman == nil || len(currentAssistantText) == 0 {
			pendingHuman = nil
			currentAssistantText = nil
			currentToolNames = nil
			return
		}
		aText := strings.Join(currentAssistantText, "\n")
		totalChars += len(aText)
		pairs = append(pairs, TurnPair{
			HumanText:    pendingHuman.text,
			AssistantText: aText,
			ToolNames:    currentToolNames,
		})
		pendingHuman = nil
		currentAssistantText = nil
		currentToolNames = nil
	}

	for i := range messages {
		msg := &messages[i]
		switch msg.lineType {
		case "user":
			flushPair()
			pendingHuman = msg

		case "assistant":
			if msg.text != "" {
				currentAssistantText = append(currentAssistantText, msg.text)
			}
			currentToolNames = append(currentToolNames, msg.toolNames...)

		case "away_summary":
			flushPair()
			totalChars += len(msg.text)
			pairs = append(pairs, TurnPair{
				HumanText:    "",
				AssistantText: msg.text,
			})
		}
	}
	flushPair()

	return pairs, totalChars
}

// ParseSubagents discovers and parses sub-agent conversations from a session directory.
// sessionDir is the bare directory alongside the .jsonl file (e.g., <uuid>/).
func ParseSubagents(sessionDir string) ([]TurnPair, error) {
	subagentsDir := filepath.Join(sessionDir, "subagents")
	entries, err := os.ReadDir(subagentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading subagents directory: %w", err)
	}

	var allPairs []TurnPair
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if !strings.HasPrefix(entry.Name(), "agent-") {
			continue
		}

		agentID := strings.TrimSuffix(entry.Name(), ".jsonl")
		metaPath := filepath.Join(subagentsDir, agentID+".meta.json")
		agentType, agentDesc := readAgentMeta(metaPath)

		jsonlPath := filepath.Join(subagentsDir, entry.Name())
		parsed, err := ParseSession(jsonlPath)
		if err != nil {
			slog.Warn("skipping sub-agent parse failure", "path", jsonlPath, "error", err)
			continue
		}

		for _, tp := range parsed.TurnPairs {
			allPairs = append(allPairs, TurnPair{
				HumanText:    tp.HumanText,
				AssistantText: tp.AssistantText,
				ToolNames:    tp.ToolNames,
				IsSubagent:   true,
				SubagentType: agentType,
				SubagentDesc: agentDesc,
			})
		}
	}

	return allPairs, nil
}

type agentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
}

func readAgentMeta(path string) (agentType, description string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var meta agentMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", ""
	}
	return meta.AgentType, meta.Description
}
