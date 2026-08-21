package vault

import (
	"fmt"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/store"
)

// chunker.go builds the semantic-chunk corpus (vault_chunks /
// vault_chunks_trigram) from the vault scanner's per-message ScanResults —
// design docs/wip/vault-session-search §D3. It deliberately does NOT import
// internal/session: the session parser is path-based and lossy-by-design,
// whereas these chunks must derive from the same broad, DB-bytes scan that
// feeds per-line vault_fts, so a chunk search and a line search can never
// disagree about what exists.

const (
	// chunkWindowTurns / chunkWindowOverlap size the sliding window in TURNS
	// (a human turn plus everything until the next one), mirroring
	// internal/session/chunk.go's defaultWindowSize/defaultOverlap so vault
	// chunks stay comparable to the knowledge.db session chunks they replace
	// (design Open Q2: reuse as-is, let benchmark A2 tune).
	chunkWindowTurns   = 4
	chunkWindowOverlap = 1
)

// Chunk is one BM25 window over a session's scanned messages — a row for both
// chunk FTS tables. ContentText is scanner output, already sanitized
// (sanitize.StripSecrets) and bounded (truncateHeadTail) per message.
type Chunk struct {
	Title          string
	ContentText    string
	SubagentID     string // "" for main-transcript chunks
	ChunkIndex     int    // sequential per session (main first, then subagents in sidecar order); stamped by scanSessionAndSubagents
	FirstLineIndex int    // raw-JSONL LineIndex of the window's first ScanResult — the TUI / `vault show` anchor
}

// chunkScanResults groups one transcript's ScanResults into overlapping
// turn-window chunks. start stamps the title's datetime (falls back to the
// first result's timestamp when zero — subagent scans discard their
// ScanOutput); subagentID tags chunks from a subagent transcript ("" for the
// main one). ChunkIndex is left zero — the caller stamps session-wide indices.
//
// A transcript whose total text fits store.MaxChunkBytes becomes a single
// full-range chunk (matching session.ChunkSession's small-session fast path);
// an oversized window is split at paragraph boundaries via store.SplitOversized,
// every part keeping the window's title and FirstLineIndex.
func chunkScanResults(results []ScanResult, start time.Time, subagentID string) []Chunk {
	if len(results) == 0 {
		return nil
	}
	if start.IsZero() {
		start = results[0].Timestamp
	}

	// Group into turns: results are emitted in order with non-decreasing
	// TurnIndex, so turns are contiguous runs.
	var turns [][]ScanResult
	for _, r := range results {
		if len(turns) == 0 || turns[len(turns)-1][0].TurnIndex != r.TurnIndex {
			turns = append(turns, []ScanResult{r})
		} else {
			turns[len(turns)-1] = append(turns[len(turns)-1], r)
		}
	}

	var total int
	for _, r := range results {
		if r.ContentText != "" { // empty texts are skipped by joinResults — don't count their separator
			total += len(r.ContentText) + 2 // +2 for the "\n\n" joins
		}
	}
	if total <= store.MaxChunkBytes {
		content := joinResults(results)
		if content == "" {
			return nil
		}
		return []Chunk{{
			Title:          buildVaultChunkTitle(start, subagentID, 0, len(turns)-1, results),
			ContentText:    content,
			SubagentID:     subagentID,
			FirstLineIndex: results[0].LineIndex,
		}}
	}

	var chunks []Chunk
	step := max(chunkWindowTurns-chunkWindowOverlap, 1)
	for startTurn := 0; startTurn < len(turns); startTurn += step {
		endTurn := min(startTurn+chunkWindowTurns-1, len(turns)-1)

		var window []ScanResult
		for t := startTurn; t <= endTurn; t++ {
			window = append(window, turns[t]...)
		}
		content := joinResults(window)
		if content == "" {
			if endTurn >= len(turns)-1 {
				break
			}
			continue
		}

		title := buildVaultChunkTitle(start, subagentID, startTurn, endTurn, window)
		firstLine := window[0].LineIndex

		if len(content) > store.MaxChunkBytes {
			for _, part := range store.SplitOversized(content, title, store.MaxChunkBytes) {
				chunks = append(chunks, Chunk{
					Title:          part.Title,
					ContentText:    part.Content,
					SubagentID:     subagentID,
					FirstLineIndex: firstLine,
				})
			}
		} else {
			chunks = append(chunks, Chunk{
				Title:          title,
				ContentText:    content,
				SubagentID:     subagentID,
				FirstLineIndex: firstLine,
			})
		}

		if endTurn >= len(turns)-1 {
			break
		}
	}
	return chunks
}

// joinResults concatenates the window's message texts, blank-line separated —
// the paragraph boundaries store.SplitOversized splits on.
func joinResults(results []ScanResult) string {
	var b strings.Builder
	for _, r := range results {
		if r.ContentText == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(r.ContentText)
	}
	return strings.TrimSpace(b.String())
}

// buildVaultChunkTitle builds a BM25-friendly chunk title from scan data,
// adapted from internal/session's buildChunkTitle for parity in the shared
// retrieval engine (the title column is indexed and BM25-weighted):
//
//	"Session <datetime> | Turns <start>-<end> | Subagent: <id> | PAL: <subtools> | Tools: <names>"
//
// The datetime is omitted when unknown; Subagent only for subagent chunks. PAL
// (mcp__pal__*) subtools are split out under their own label, other tools
// dedup in first-appearance order — both exactly as the session title builder
// does.
func buildVaultChunkTitle(start time.Time, subagentID string, startTurn, endTurn int, window []ScanResult) string {
	var b strings.Builder

	if !start.IsZero() {
		fmt.Fprintf(&b, "Session %s | ", start.UTC().Format("2006-01-02T15:04:05Z"))
	}
	fmt.Fprintf(&b, "Turns %d-%d", startTurn+1, endTurn+1)

	if subagentID != "" {
		fmt.Fprintf(&b, " | Subagent: %s", subagentID)
	}

	palSet := make(map[string]bool)
	var palOrder []string
	toolSet := make(map[string]bool)
	var toolOrder []string
	for _, r := range window {
		for _, name := range r.ToolNames {
			if subtool, ok := strings.CutPrefix(name, "mcp__pal__"); ok {
				if !palSet[subtool] {
					palSet[subtool] = true
					palOrder = append(palOrder, subtool)
				}
			} else if !toolSet[name] {
				toolSet[name] = true
				toolOrder = append(toolOrder, name)
			}
		}
	}

	if len(palOrder) > 0 {
		fmt.Fprintf(&b, " | PAL: %s", strings.Join(palOrder, ", "))
	}
	if len(toolOrder) > 0 {
		fmt.Fprintf(&b, " | Tools: %s", strings.Join(toolOrder, ", "))
	}

	return b.String()
}

// chunkContentBytes sums the bytes a chunk set adds to a write transaction.
// Each chunk row is inserted into BOTH layer tables (porter + trigram), hence
// the ×2 — used by the reindex/import batch-size bounds alongside
// ftsContentBytes.
func chunkContentBytes(chunks []Chunk) int64 {
	var n int64
	for _, c := range chunks {
		n += 2 * int64(len(c.Title)+len(c.ContentText))
	}
	return n
}
