package store

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxChunkBytes = 4096

var (
	headingRe   = regexp.MustCompile(`^(#{1,4})\s+(.+)$`)
	horizRuleRe = regexp.MustCompile(`^[-_*]{3,}\s*$`)
	codeFenceRe = regexp.MustCompile("^`{3,}")
	blankLineRe = regexp.MustCompile(`\n\s*\n`)
	codeBlockRe = regexp.MustCompile("(?s)```\\w*\n[\\s\\S]*?```")
)

// chunkMarkdown splits markdown content by headings with code-block awareness.
func chunkMarkdown(content string, maxBytes int) []Chunk {
	if maxBytes <= 0 {
		maxBytes = MaxChunkBytes
	}

	lines := strings.Split(content, "\n")
	var chunks []Chunk
	var currentLines []string
	var headingStack [4]string // H1-H4
	inFence := false

	flush := func() {
		if len(currentLines) == 0 {
			return
		}
		text := strings.Join(currentLines, "\n")
		text = strings.TrimSpace(text)
		if text == "" {
			currentLines = nil
			return
		}
		title := buildHeadingTitle(headingStack)
		hasCode := ChunkHasCode(text)
		if len(text) <= maxBytes {
			chunks = append(chunks, Chunk{Title: title, Content: text, HasCode: hasCode})
		} else {
			chunks = append(chunks, SplitOversized(text, title, maxBytes)...)
		}
		currentLines = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Track code fence state.
		if codeFenceRe.MatchString(trimmed) {
			inFence = !inFence
			currentLines = append(currentLines, line)
			continue
		}

		if inFence {
			currentLines = append(currentLines, line)
			continue
		}

		// Horizontal rule — flush.
		if horizRuleRe.MatchString(trimmed) {
			flush()
			continue
		}

		// Heading — flush and update stack.
		if m := headingRe.FindStringSubmatch(trimmed); m != nil {
			flush()
			level := len(m[1]) // 1-4
			headingStack[level-1] = m[2]
			// Clear deeper levels.
			for i := level; i < 4; i++ {
				headingStack[i] = ""
			}
			continue
		}

		currentLines = append(currentLines, line)
	}
	flush()

	if len(chunks) == 0 && strings.TrimSpace(content) != "" {
		chunks = append(chunks, Chunk{
			Title:   "Content",
			Content: strings.TrimSpace(content),
			HasCode: ChunkHasCode(content),
		})
	}

	return chunks
}

func buildHeadingTitle(stack [4]string) string {
	var parts []string
	for _, h := range stack {
		if h != "" {
			parts = append(parts, h)
		}
	}
	if len(parts) == 0 {
		return "Content"
	}
	return strings.Join(parts, " > ")
}

// SplitOversized splits a chunk that exceeds maxBytes at paragraph boundaries.
func SplitOversized(text, title string, maxBytes int) []Chunk {
	paragraphs := blankLineRe.Split(text, -1)
	var chunks []Chunk
	var buf strings.Builder
	overlap := ""

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if buf.Len() > 0 && buf.Len()+len(para)+2 > maxBytes {
			content := buf.String()
			chunks = append(chunks, Chunk{
				Title:   title,
				Content: content,
				HasCode: ChunkHasCode(content),
			})
			// Keep last 2 lines as overlap.
			lines := strings.Split(content, "\n")
			if len(lines) > 2 {
				overlap = strings.Join(lines[len(lines)-2:], "\n")
			} else {
				overlap = content
			}
			buf.Reset()
			if overlap != "" {
				buf.WriteString(overlap)
				buf.WriteString("\n\n")
			}
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(para)
	}

	if buf.Len() > 0 {
		content := buf.String()
		chunks = append(chunks, Chunk{
			Title:   title,
			Content: content,
			HasCode: ChunkHasCode(content),
		})
	}
	return chunks
}

func ChunkHasCode(content string) bool {
	return codeBlockRe.MatchString(content)
}

// chunkPlainText splits plain text using blank-line or fixed-line strategies.
func chunkPlainText(content string, linesPerChunk int) []Chunk {
	if linesPerChunk <= 0 {
		linesPerChunk = 20
	}

	lines := strings.Split(content, "\n")

	// Short outputs keep one logical section, split further only by bytes.
	if len(lines) <= linesPerChunk {
		return appendPlainTextChunks(nil, content, "Output")
	}

	// Phase 1: try blank-line splitting.
	sections := blankLineRe.Split(content, -1)
	if len(sections) >= 3 && len(sections) <= 200 && allUnder(sections, 5000) {
		var chunks []Chunk
		for i, sec := range sections {
			sec = strings.TrimSpace(sec)
			if sec == "" {
				continue
			}
			title := firstLine(sec, 80)
			if title == "" {
				title = fmt.Sprintf("Section %d", i+1)
			}
			chunks = appendPlainTextChunks(chunks, sec, title)
		}
		return chunks
	}

	// Phase 2: fixed-line groups with overlap.
	const overlap = 2
	var chunks []Chunk
	for i := 0; i < len(lines); {
		end := min(i+linesPerChunk, len(lines))
		slice := lines[i:end]
		text := strings.Join(slice, "\n")
		text = strings.TrimSpace(text)
		if text != "" {
			title := firstLine(text, 80)
			chunks = appendPlainTextChunks(chunks, text, title)
		}
		if end == len(lines) {
			break
		}
		next := max(end-overlap, i+1)
		// Preserve ordinary two-line overlap, but do not emit an entire
		// oversized line again after splitting it into bounded chunks.
		for j := next; j < end; j++ {
			if len(lines[j]) > MaxChunkBytes {
				next = j + 1
			}
		}
		i = next
	}
	return chunks
}

// appendPlainTextChunks bounds a strategy's output without adding overlap. Prefer
// line boundaries, then whitespace, then a UTF-8 boundary. Only whitespace is
// removed. Append directly to avoid allocating a temporary slice per section.
func appendPlainTextChunks(chunks []Chunk, text, title string) []Chunk {
	text = strings.TrimSpace(text)
	start := len(chunks)
	for len(text) > MaxChunkBytes {
		end := len(utf8Prefix(text, MaxChunkBytes))
		if i := strings.LastIndexByte(text[:end], '\n'); i > 0 {
			end = i
		} else if i := strings.LastIndexFunc(text[:end], unicode.IsSpace); i > 0 {
			end = i
		}
		chunks = append(chunks, Chunk{Title: title, Content: strings.TrimSpace(text[:end])})
		text = strings.TrimSpace(text[end:])
	}
	if text != "" || len(chunks) == start {
		chunks = append(chunks, Chunk{Title: title, Content: text})
	}
	return chunks
}

// utf8Prefix caps bytes while preserving valid UTF-8 input. Invalid input is
// retained as bytes, with forward progress even for runs of continuation bytes.
func utf8Prefix(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	if end == 0 {
		end = maxBytes
	}
	return text[:end]
}

func allUnder(sections []string, maxBytes int) bool {
	for _, s := range sections {
		if len(s) > maxBytes {
			return false
		}
	}
	return true
}

func firstLine(text string, maxLen int) string {
	line, _, _ := strings.Cut(text, "\n")
	line = strings.TrimSpace(line)
	return utf8Prefix(line, maxLen)
}

// walkJSON recursively chunks a parsed JSON value.
func walkJSON(v any, path []string, maxBytes int) []Chunk {
	if maxBytes <= 0 {
		maxBytes = MaxChunkBytes
	}

	switch val := v.(type) {
	case map[string]any:
		return chunkJSONObject(val, path, maxBytes)
	case []any:
		prefix := "(root)"
		if len(path) > 0 {
			prefix = strings.Join(path, " > ")
		}
		return chunkJSONArray(val, prefix, maxBytes)
	default:
		// Primitive.
		serialized, _ := json.Marshal(val)
		if len(serialized) > maxBytes {
			title := "(root)"
			if len(path) > 0 {
				title = strings.Join(path, " > ")
			}
			return []Chunk{{Title: title, Content: string(serialized)}}
		}
		return nil
	}
}

func chunkJSONObject(obj map[string]any, path []string, maxBytes int) []Chunk {
	// Check if flat (no nested objects/arrays).
	flat := true
	for _, v := range obj {
		switch v.(type) {
		case map[string]any, []any:
			flat = false
		}
		if !flat {
			break
		}
	}

	serialized, _ := json.MarshalIndent(obj, "", "  ")

	if flat && len(serialized) < maxBytes {
		title := "(root)"
		if len(path) > 0 {
			title = strings.Join(path, " > ")
		}
		return []Chunk{{Title: title, Content: string(serialized), HasCode: true}}
	}

	// Recurse into each key in sorted order for deterministic output.
	var chunks []Chunk
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		val := obj[key]
		childPath := append(append([]string{}, path...), key)
		sub := walkJSON(val, childPath, maxBytes)
		if len(sub) == 0 {
			// Small primitive — emit inline.
			s, _ := json.Marshal(val)
			title := strings.Join(childPath, " > ")
			chunks = append(chunks, Chunk{Title: title, Content: string(s), HasCode: true})
		} else {
			chunks = append(chunks, sub...)
		}
	}
	return chunks
}

func chunkJSONArray(arr []any, prefix string, maxBytes int) []Chunk {
	if len(arr) == 0 {
		return nil
	}

	idField := findIdentityField(arr)
	var chunks []Chunk
	var batch []any
	batchSize := 0
	startIdx := 0

	flushBatch := func(endIdx int) {
		if len(batch) == 0 {
			return
		}
		serialized, _ := json.MarshalIndent(batch, "", "  ")
		title := jsonBatchTitle(prefix, batch, idField, startIdx, endIdx)
		chunks = append(chunks, Chunk{Title: title, Content: string(serialized), HasCode: true})
	}

	for i, item := range arr {
		s, _ := json.Marshal(item)
		itemSize := len(s)

		if batchSize > 0 && batchSize+itemSize > maxBytes && len(batch) > 0 {
			flushBatch(startIdx + len(batch) - 1)
			batch = nil
			batchSize = 0
			startIdx = i
		}

		batch = append(batch, item)
		batchSize += itemSize
	}
	flushBatch(startIdx + len(batch) - 1)

	return chunks
}

func findIdentityField(arr []any) string {
	if len(arr) == 0 {
		return ""
	}
	obj, ok := arr[0].(map[string]any)
	if !ok {
		return ""
	}
	candidates := []string{"id", "name", "title", "path", "slug", "key", "label"}
	for _, field := range candidates {
		if v, exists := obj[field]; exists {
			switch v.(type) {
			case string, float64, int:
				return field
			}
		}
	}
	return ""
}

func jsonBatchTitle(prefix string, batch []any, idField string, startIdx, endIdx int) string {
	if idField != "" {
		ids := extractIDs(batch, idField)
		switch len(ids) {
		case 1:
			return fmt.Sprintf("%s > %s", prefix, ids[0])
		case 2, 3:
			return fmt.Sprintf("%s > %s", prefix, strings.Join(ids, ", "))
		default:
			if len(ids) > 0 {
				return fmt.Sprintf("%s > %s…%s", prefix, ids[0], ids[len(ids)-1])
			}
		}
	}
	if startIdx == endIdx {
		return fmt.Sprintf("%s > [%d]", prefix, startIdx)
	}
	return fmt.Sprintf("%s > [%d-%d]", prefix, startIdx, endIdx)
}

func extractIDs(batch []any, field string) []string {
	var ids []string
	for _, item := range batch {
		if obj, ok := item.(map[string]any); ok {
			if v, exists := obj[field]; exists {
				ids = append(ids, fmt.Sprintf("%v", v))
			}
		}
	}
	return ids
}
