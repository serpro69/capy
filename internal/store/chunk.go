package store

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

const maxChunkBytes = 4096

var (
	headingRe    = regexp.MustCompile(`^(#{1,4})\s+(.+)$`)
	horizRuleRe  = regexp.MustCompile(`^[-_*]{3,}\s*$`)
	codeFenceRe  = regexp.MustCompile("^`{3,}")
	blankLineRe  = regexp.MustCompile(`\n\s*\n`)
	codeBlockRe  = regexp.MustCompile("(?s)```\\w*\n[\\s\\S]*?```")
)

// chunkMarkdown splits markdown content by headings with code-block awareness.
func chunkMarkdown(content string, maxBytes int) []Chunk {
	if maxBytes <= 0 {
		maxBytes = maxChunkBytes
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
		hasCode := chunkHasCode(text)
		if len(text) <= maxBytes {
			chunks = append(chunks, Chunk{Title: title, Content: text, HasCode: hasCode})
		} else {
			chunks = append(chunks, splitOversized(text, title, maxBytes)...)
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
			HasCode: chunkHasCode(content),
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

// splitOversized splits a chunk that exceeds maxBytes at paragraph boundaries.
func splitOversized(text, title string, maxBytes int) []Chunk {
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
				HasCode: chunkHasCode(content),
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
			HasCode: chunkHasCode(content),
		})
	}
	return chunks
}

func chunkHasCode(content string) bool {
	return codeBlockRe.MatchString(content)
}

// chunkPlainText splits plain text using blank-line or fixed-line strategies.
func chunkPlainText(content string, linesPerChunk int) []Chunk {
	if linesPerChunk <= 0 {
		linesPerChunk = 20
	}

	lines := strings.Split(content, "\n")

	// Single chunk if small enough.
	if len(lines) <= linesPerChunk {
		return []Chunk{{Title: "Output", Content: strings.TrimSpace(content)}}
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
			chunks = append(chunks, Chunk{Title: title, Content: sec})
		}
		return chunks
	}

	// Phase 2: fixed-line groups with overlap.
	const overlap = 2
	step := max(linesPerChunk-overlap, 1)
	var chunks []Chunk
	for i := 0; i < len(lines); i += step {
		end := min(i+linesPerChunk, len(lines))
		slice := lines[i:end]
		text := strings.Join(slice, "\n")
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		title := firstLine(text, 80)
		if title == "" {
			title = fmt.Sprintf("Lines %d-%d", i+1, end)
		}
		chunks = append(chunks, Chunk{Title: title, Content: text})
		if end == len(lines) {
			break
		}
	}
	return chunks
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
	if len(line) > maxLen {
		line = line[:maxLen]
	}
	return line
}

// walkJSON recursively chunks a parsed JSON value.
func walkJSON(v any, path []string, maxBytes int) []Chunk {
	if maxBytes <= 0 {
		maxBytes = maxChunkBytes
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
