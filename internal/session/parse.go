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
	ToolMeta     []string
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
	noiseTagRe = regexp.MustCompile(`(?s)<local-command-caveat>.*?</local-command-caveat>|<local-command-stdout>.*?</local-command-stdout>|<command-name>.*?</command-name>|<command-message>.*?</command-message>|<command-args>.*?</command-args>`)
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
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	ID    string          `json:"id"`
	Input json.RawMessage `json:"input"`
}

// parsedMessage is an intermediate representation after JSONL deduplication.
type parsedMessage struct {
	lineType  string // "user", "assistant", "away_summary"
	timestamp time.Time
	sessionID string
	text      string   // extracted human/assistant text
	toolNames []string // tool_use names (assistant only)
	toolMeta  []string // enriched tool metadata lines (assistant only)
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

	// First pass: parse all lines and merge assistant progressive snapshots
	// by message.id. Claude Code writes one content block per JSONL line
	// (non-cumulative), so we collect all blocks sharing the same message.id.
	type rawEntry struct {
		line         jsonlLine
		msgID        string // message.id for assistant merge; uuid for others
		rawMsg       jsonlMessage
		hasMsgP      bool           // whether Message field was present and parsed
		mergedBlocks []contentBlock // accumulated blocks from all progressive snapshots
	}

	var entries []rawEntry
	assistantLastIdx := make(map[string]int) // message.id → index in entries

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
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

		entry := rawEntry{line: line}

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

			var blocks []contentBlock
			if entry.hasMsgP && len(entry.rawMsg.Content) > 0 {
				if err := json.Unmarshal(entry.rawMsg.Content, &blocks); err != nil {
					slog.Warn("skipping malformed assistant content", "file", path, "line", lineNum, "error", err)
				}
			}

			if prevIdx, exists := assistantLastIdx[msgID]; exists {
				target := &entries[prevIdx]
				for _, nb := range blocks {
					dup := false
					for _, eb := range target.mergedBlocks {
						if eb.Type == nb.Type && eb.Text == nb.Text && eb.Name == nb.Name && eb.ID == nb.ID {
							dup = true
							break
						}
					}
					if !dup {
						target.mergedBlocks = append(target.mergedBlocks, nb)
					}
				}
			} else {
				entry.mergedBlocks = blocks
				entry.rawMsg.Content = nil
				assistantLastIdx[msgID] = len(entries)
				entries = append(entries, entry)
			}
		case "system":
			if line.Subtype == "away_summary" {
				entry.msgID = line.UUID
				entries = append(entries, entry)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("scanner error, processing lines read so far", "file", path, "line", lineNum, "error", err)
	}

	// Second pass: extract text from deduplicated entries and build messages.
	var messages []parsedMessage
	var startTime time.Time
	foundSessionID := sessionID

	for _, entry := range entries {
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
			if len(entry.mergedBlocks) == 0 {
				continue
			}
			text, toolNames, toolMeta := extractAssistantBlocks(entry.mergedBlocks)
			if text == "" && len(toolNames) == 0 {
				continue
			}
			messages = append(messages, parsedMessage{
				lineType:  "assistant",
				timestamp: ts,
				sessionID: foundSessionID,
				text:      text,
				toolNames: toolNames,
				toolMeta:  toolMeta,
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

// cleanUserText strips noise tags from user message text.
func cleanUserText(text string) string {
	text = sysReminderRe.ReplaceAllString(text, "")
	text = noiseTagRe.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)
	return text
}

// extractAssistantBlocks extracts text, tool names, and enriched tool metadata
// from pre-parsed content blocks using the extractor registry.
func extractAssistantBlocks(blocks []contentBlock) (string, []string, []string) {
	var texts []string
	var toolNames []string
	var toolMeta []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			t := strings.TrimSpace(b.Text)
			if t != "" {
				texts = append(texts, t)
			}
		case "tool_use":
			ext, ok := DefaultRegistry.Lookup(b.Name)
			if !ok {
				continue
			}
			extracted := ext.Extract(b.Input)
			switch ext.Action {
			case ActionPromote:
				if extracted != "" {
					texts = append(texts, extracted)
					toolNames = append(toolNames, b.Name)
				}
			case ActionEnrich:
				if extracted != "" {
					toolMeta = append(toolMeta, extracted)
				} else {
					// Record that the tool was called even without extractable detail.
					toolMeta = append(toolMeta, fmt.Sprintf("[%s]", b.Name))
				}
				toolNames = append(toolNames, b.Name)
			}
		}
	}
	return strings.Join(texts, "\n"), toolNames, toolMeta
}

// buildTurnPairs groups sequential user→assistant messages into pairs.
// Away summaries are emitted as standalone entries (empty HumanText).
func buildTurnPairs(messages []parsedMessage) ([]TurnPair, int) {
	var pairs []TurnPair
	totalChars := 0

	var pendingHuman *parsedMessage
	var currentAssistantText []string
	var currentToolNames []string
	var currentToolMeta []string

	flushPair := func() {
		if pendingHuman == nil || len(currentAssistantText) == 0 {
			pendingHuman = nil
			currentAssistantText = nil
			currentToolNames = nil
			currentToolMeta = nil
			return
		}
		aText := strings.Join(currentAssistantText, "\n")
		totalChars += len(aText)
		pairs = append(pairs, TurnPair{
			HumanText:    pendingHuman.text,
			AssistantText: aText,
			ToolNames:    currentToolNames,
			ToolMeta:     currentToolMeta,
		})
		pendingHuman = nil
		currentAssistantText = nil
		currentToolNames = nil
		currentToolMeta = nil
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
			currentToolMeta = append(currentToolMeta, msg.toolMeta...)

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
				ToolMeta:     tp.ToolMeta,
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
