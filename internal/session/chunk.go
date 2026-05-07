package session

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/serpro69/capy/internal/store"
)

const (
	defaultWindowSize = 4
	defaultOverlap    = 1
)

// ChunkSession splits a parsed session into store-ready chunks using a sliding
// window over turn pairs. Titles are built from structured ParsedSession data
// (timestamps, tool names, subagent info), not parsed from transcript text.
//
// The transcript parameter must be the result of BuildTranscript — its Offsets
// are used to slice content by turn pair boundaries.
// maxBytes controls the maximum chunk size; 0 uses store.MaxChunkBytes.
//
// The caller is responsible for sanitizing the transcript text (via sanitize.StripSecrets)
// before calling this function if the content will be indexed.
func ChunkSession(session *ParsedSession, tr TranscriptResult, maxBytes int) []store.Chunk {
	if maxBytes <= 0 {
		maxBytes = store.MaxChunkBytes
	}

	pairs := session.TurnPairs
	if len(pairs) == 0 {
		return nil
	}

	transcript := tr.Text

	if len(transcript) <= maxBytes {
		title := buildChunkTitle(session, 0, len(pairs)-1)
		return []store.Chunk{{
			Title:   title,
			Content: strings.TrimSpace(transcript),
			HasCode: store.ChunkHasCode(transcript),
		}}
	}

	var chunks []store.Chunk
	step := max(defaultWindowSize-defaultOverlap, 1)

	for start := 0; start < len(pairs); start += step {
		end := min(start+defaultWindowSize-1, len(pairs)-1)

		content := extractWindowContent(transcript, tr.Offsets, start, end)
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}

		title := buildChunkTitle(session, start, end)

		if len(content) > maxBytes {
			chunks = append(chunks, store.SplitOversized(content, title, maxBytes)...)
		} else {
			chunks = append(chunks, store.Chunk{
				Title:   title,
				Content: content,
				HasCode: store.ChunkHasCode(content),
			})
		}

		if end >= len(pairs)-1 {
			break
		}
	}

	return chunks
}

// extractWindowContent returns the transcript text for turn pairs [start, end].
func extractWindowContent(transcript string, offsets []TurnOffset, start, end int) string {
	if start >= len(offsets) {
		return ""
	}
	end = min(end, len(offsets)-1)
	lo := offsets[start].Start
	hi := min(offsets[end].End, len(transcript))
	return transcript[lo:hi]
}

// buildChunkTitle builds a BM25-friendly title from structured session data.
// Format: "Session <datetime> | Turns <start>-<end> | PAL: <subtool> | Tools: <names>"
// With subagent: "Session <datetime> | Turns <start>-<end> | Subagent: <type> | PAL: <subtool> | Tools: <names>"
// PAL tools (mcp__pal__ prefix) are separated under a PAL: label; other tools under Tools:.
func buildChunkTitle(session *ParsedSession, start, end int) string {
	var b strings.Builder

	ts := session.StartTime.UTC().Format("2006-01-02T15:04:05Z")
	fmt.Fprintf(&b, "Session %s | Turns %d-%d", ts, start+1, end+1)

	subagentTypes := make(map[string]bool)
	palSet := make(map[string]bool)
	var palOrder []string
	toolSet := make(map[string]bool)
	var toolOrder []string

	for i := start; i <= end && i < len(session.TurnPairs); i++ {
		tp := session.TurnPairs[i]
		if tp.IsSubagent && tp.SubagentType != "" {
			subagentTypes[tp.SubagentType] = true
		}
		for _, name := range tp.ToolNames {
			if subtool, ok := strings.CutPrefix(name, "mcp__pal__"); ok {
				if !palSet[subtool] {
					palSet[subtool] = true
					palOrder = append(palOrder, subtool)
				}
			} else {
				if !toolSet[name] {
					toolSet[name] = true
					toolOrder = append(toolOrder, name)
				}
			}
		}
	}

	if len(subagentTypes) > 0 {
		types := slices.Sorted(maps.Keys(subagentTypes))
		fmt.Fprintf(&b, " | Subagent: %s", strings.Join(types, ", "))
	}

	if len(palOrder) > 0 {
		fmt.Fprintf(&b, " | PAL: %s", strings.Join(palOrder, ", "))
	}

	if len(toolOrder) > 0 {
		fmt.Fprintf(&b, " | Tools: %s", strings.Join(toolOrder, ", "))
	}

	return b.String()
}
