package session

import (
	"fmt"
	"strings"
)

// TranscriptResult holds the plaintext transcript and a per-turn-pair byte offset
// map, so that chunking can slice by turn pair boundaries without re-parsing.
type TranscriptResult struct {
	Text    string
	Offsets []TurnOffset
}

// TurnOffset records the byte range [Start, End) of a turn pair in the transcript.
type TurnOffset struct {
	Start int
	End   int
}

// BuildTranscript converts a ParsedSession into a plaintext transcript string
// with Human:/Assistant: format, [Tools: ...] lines, [Session summary: ...] entries,
// and --- Subagent --- delimiters. Consecutive subagent turns from the same agent
// are grouped under a single delimiter block.
//
// Returns the transcript text and per-turn-pair byte offsets so chunking can
// slice content by turn pair boundaries without re-serializing.
func BuildTranscript(s *ParsedSession) TranscriptResult {
	var b strings.Builder
	offsets := make([]TurnOffset, len(s.TurnPairs))
	n := len(s.TurnPairs)

	for i := 0; i < n; {
		if i > 0 {
			b.WriteByte('\n')
		}

		tp := s.TurnPairs[i]

		if tp.IsSubagent {
			i = writeSubagentBlock(&b, s.TurnPairs, i, offsets)
			continue
		}

		start := b.Len()

		if tp.HumanText == "" && tp.AssistantText != "" {
			fmt.Fprintf(&b, "[Session summary: %s]\n", tp.AssistantText)
		} else if tp.HumanText == "" && tp.AssistantText == "" {
			i++
			offsets[i-1] = TurnOffset{Start: start, End: start}
			continue
		} else {
			fmt.Fprintf(&b, "Human: %s\n", tp.HumanText)
			fmt.Fprintf(&b, "Assistant: %s\n", tp.AssistantText)
			if len(tp.ToolNames) > 0 {
				fmt.Fprintf(&b, "[Tools: %s]\n", strings.Join(tp.ToolNames, ", "))
			}
		}

		offsets[i] = TurnOffset{Start: start, End: b.Len()}
		i++
	}

	return TranscriptResult{Text: b.String(), Offsets: offsets}
}

// writeSubagentBlock writes a group of consecutive subagent turns sharing the same
// SubagentType+SubagentDesc under a single delimiter pair. Records offsets for each
// turn. Returns the index of the first turn pair after the block.
func writeSubagentBlock(b *strings.Builder, pairs []TurnPair, start int, offsets []TurnOffset) int {
	tp := pairs[start]
	agentType := tp.SubagentType
	if agentType == "" {
		agentType = "Agent"
	}
	desc := tp.SubagentDesc
	if desc == "" {
		desc = "subagent task"
	}

	blockStart := b.Len()
	fmt.Fprintf(b, "--- Subagent: %s — %q ---\n", agentType, desc)

	i := start
	for i < len(pairs) && pairs[i].IsSubagent &&
		pairs[i].SubagentType == tp.SubagentType &&
		pairs[i].SubagentDesc == tp.SubagentDesc {

		turnStart := b.Len()
		if i > start {
			b.WriteByte('\n')
			turnStart = b.Len()
		}

		fmt.Fprintf(b, "Human: %s\n", pairs[i].HumanText)
		fmt.Fprintf(b, "Assistant: %s\n", pairs[i].AssistantText)
		if len(pairs[i].ToolNames) > 0 {
			fmt.Fprintf(b, "[Tools: %s]\n", strings.Join(pairs[i].ToolNames, ", "))
		}

		// For the first turn in the group, include the opening delimiter.
		if i == start {
			offsets[i] = TurnOffset{Start: blockStart, End: b.Len()}
		} else {
			offsets[i] = TurnOffset{Start: turnStart, End: b.Len()}
		}
		i++
	}

	b.WriteString("--- End subagent ---\n")

	// Extend the last turn's offset to include the closing delimiter.
	if i > start {
		offsets[i-1] = TurnOffset{Start: offsets[i-1].Start, End: b.Len()}
	}

	return i
}
