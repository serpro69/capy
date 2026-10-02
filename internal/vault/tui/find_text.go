package tui

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/vault"
)

const findCheckBytes = 4096

type findField uint8

const (
	findSummary findField = iota + 1 // summaries precede bodies in corpus order
	findBody
)

// Positions use message ordinals, never SourceLine (several display messages
// can originate in one JSONL record). Offsets are bytes in the original field.
// A position is interpreted only within its immutable corpus's scope.
type findPosition struct {
	message int
	field   findField
	offset  int
}

func (p findPosition) before(q findPosition) bool {
	if p.message != q.message {
		return p.message < q.message
	}
	if p.field != q.field {
		return p.field < q.field
	}
	return p.offset < q.offset
}

type findLine struct {
	position findPosition
	ordinal  int // zero-based content line within the field
	text     string
	hidden   bool
}

// Corpus, lines, and result slices are immutable after a worker publishes them.
// Text references the already-parsed messages; it is neither copied nor indexed.
type findCorpus struct {
	scope string
	lines []findLine
}

type findHit struct {
	line       int // index in corpus.lines, also the stable corpus ordering
	start, end int // half-open original field byte offsets
}

func (c *findCorpus) position(hit findHit) findPosition {
	p := c.lines[hit.line].position
	p.offset = hit.start
	return p
}

func buildFindCorpus(ctx context.Context, scope string, messages []vault.TranscriptMessage) (*findCorpus, error) {
	c := &findCorpus{scope: scope, lines: make([]findLine, 0, len(messages))}
	for i, msg := range messages {
		if msg.Collapsed {
			if err := c.addField(ctx, i, findSummary, msg.ToolSummary, false); err != nil {
				return nil, err
			}
		}
		if err := c.addField(ctx, i, findBody, msg.Body, msg.Collapsed); err != nil {
			return nil, err
		}
	}
	return c, ctx.Err()
}

func (c *findCorpus) addField(ctx context.Context, message int, field findField, text string, hidden bool) error {
	start, ordinal := 0, 0
	appendLine := func(end int) {
		c.lines = append(c.lines, findLine{findPosition{message, field, start}, ordinal, text[start:end], hidden})
		start, ordinal = end+1, ordinal+1
	}
	for offset := 0; offset < len(text); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(len(text), offset+findCheckBytes)
		for offset < end {
			n := strings.IndexByte(text[offset:end], '\n')
			if n < 0 {
				break
			}
			appendLine(offset + n)
			offset += n + 1
		}
		offset = end
	}
	appendLine(len(text)) // preserve empty fields and trailing empty lines
	return ctx.Err()
}

// findExact scans every field, including collapsed bodies. Presentation scope
// restrictions belong to the caller, not to corpus construction or matching.
func findExact(ctx context.Context, c *findCorpus, query string) ([]findHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if query == "" || strings.ContainsRune(query, '\n') {
		return nil, nil
	}
	var hits []findHit
	for i, line := range c.lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		nextCheck := 0
		for offset := 0; offset < len(line.text); {
			if offset >= nextCheck {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				nextCheck = offset + findCheckBytes
			}
			if strings.HasPrefix(line.text[offset:], query) {
				start := line.position.offset + offset
				hits = append(hits, findHit{i, start, start + len(query)})
			}
			_, size := utf8.DecodeRuneInString(line.text[offset:])
			offset += size // advance from the start, retaining overlapping matches
		}
	}
	return hits, ctx.Err()
}
