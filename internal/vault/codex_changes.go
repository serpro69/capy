package vault

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// codexPaginatedFileChange normalizes recorded evidence without deciding which
// candidate diffs the viewer may present as completed changes.
func codexPaginatedFileChange(item codexTurnItem) *FileChangeSet {
	s := &FileChangeSet{ID: item.ID, State: FileChangeUnconfirmed}
	status, ok := asJSONString(item.Status)
	if ok {
		switch FileChangeState(status) {
		case FileChangeCompleted, FileChangeFailed, FileChangeDeclined:
			s.State = FileChangeState(status)
		}
	}
	if s.State == FileChangeUnconfirmed {
		s.Diagnostics = append(s.Diagnostics, changeDiagnostic("status", "status", item.Status,
			"Completion status is missing or unrecognized; changes are unconfirmed."))
	}
	s.Stdout = codexChangeOutput(item.Stdout, "stdout", &s.Diagnostics)
	s.Stderr = codexChangeOutput(item.Stderr, "stderr", &s.Diagnostics)
	var changes map[string]json.RawMessage
	if !isJSONObject(item.Changes) || json.Unmarshal(item.Changes, &changes) != nil {
		s.Diagnostics = append(s.Diagnostics, changeDiagnostic("changes", "changes", item.Changes,
			"Recorded changes data is unavailable or malformed."))
		return s
	}
	s.ChangesAvailable = true
	paths := make([]string, 0, len(changes))
	for path := range changes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	s.Files = make([]FileChange, 0, len(paths))
	for _, path := range paths {
		s.Files = append(s.Files, codexNormalizeFileChange(path, changes[path]))
	}
	return s
}

func changeDiagnostic(code, field string, raw json.RawMessage, message string) FileChangeDiagnostic {
	// Compare malformed values independently of JSON whitespace/member order,
	// without rounding numbers or normalizing bytes inside recorded strings.
	value := string(raw)
	var decoded any
	d := json.NewDecoder(strings.NewReader(value))
	d.UseNumber()
	if d.Decode(&decoded) == nil {
		if encoded, err := json.Marshal(decoded); err == nil {
			value = string(encoded)
		}
	}
	return FileChangeDiagnostic{Code: code, Field: field, Value: value, Message: message}
}

func codexChangeOutput(raw json.RawMessage, field string, diagnostics *[]FileChangeDiagnostic) string {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return ""
	}
	if text, ok := asJSONString(raw); ok {
		return text
	}
	*diagnostics = append(*diagnostics, changeDiagnostic("output", field, raw, "Recorded "+field+" is not text."))
	return ""
}

func codexNormalizeFileChange(path string, raw json.RawMessage) FileChange {
	f := FileChange{Path: path}
	var wire codexFileChange
	if !isJSONObject(raw) || json.Unmarshal(raw, &wire) != nil {
		f.Diagnostics = append(f.Diagnostics, changeDiagnostic("file", "changes", raw, "Recorded file change is malformed."))
		return f
	}
	f.Kind, _ = asJSONString(wire.Type)
	switch f.Kind {
	case "add", "delete":
		content, ok := codexChangeString(wire.Content)
		if !ok {
			f.Diagnostics = append(f.Diagnostics, changeDiagnostic("content", "content", wire.Content,
				"Recorded file content is missing or is not text."))
			return f
		}
		f.Content = content
		f.Diff = codexContentDiff(content, f.Kind == "add")
		return f
	case "update":
		// Unified hunks and an optional move destination are handled below.
	default:
		f.Content, _ = asJSONString(wire.Content)
		f.Diagnostics = append(f.Diagnostics, changeDiagnostic("operation", "type", wire.Type,
			"Diff conversion for this file operation is unavailable."))
		return f
	}
	text, ok := codexChangeString(wire.UnifiedDiff)
	if !ok {
		f.Diagnostics = append(f.Diagnostics, changeDiagnostic("diff", "unified_diff", wire.UnifiedDiff,
			"Recorded update diff is missing or is not text."))
	} else {
		f.Content = text
	}
	if len(wire.MovePath) > 0 && strings.TrimSpace(string(wire.MovePath)) != "null" {
		destination, valid := asJSONString(wire.MovePath)
		if !valid || destination == "" {
			f.Diagnostics = append(f.Diagnostics, changeDiagnostic("destination", "move_path", wire.MovePath,
				"Recorded move destination is malformed."))
		} else {
			f.MovePath = destination
		}
	}
	if len(f.Diagnostics) > 0 {
		return f
	}
	// An explicitly empty diff records zero changed lines (a pure rename when
	// a destination is present, otherwise a no-op). Missing/null was rejected.
	if text == "" {
		f.Diff = &Diff{}
		return f
	}
	diff, reason := codexUpdateDiff(text)
	if reason != "" {
		f.Diagnostics = append(f.Diagnostics, changeDiagnostic("hunk", "unified_diff", nil, reason))
		return f
	}
	f.Diff = diff
	return f
}

// Unlike optional output/destination fields, content must distinguish JSON null
// from an explicitly recorded empty file. The shared string decoder accepts
// null as Go's empty string for compatibility with older transcript consumers.
func codexChangeString(raw json.RawMessage) (string, bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "", false
	}
	return asJSONString(raw)
}

// codexContentDiff derives exact ranges from archived add/delete content. A
// trailing newline terminates a line; it does not create an extra empty line.
// Preserve CR bytes, and make a missing final newline explicit in the diff.
func codexContentDiff(content string, added bool) *Diff {
	d := &Diff{}
	if content == "" {
		return d
	}
	n := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		n++
	}
	var body strings.Builder
	prefix := byte('-')
	if added {
		d.Added = n
		prefix = '+'
		body.WriteString("@@ -0,0 +1," + strconv.Itoa(n) + " @@\n")
	} else {
		d.Removed = n
		body.WriteString("@@ -1," + strconv.Itoa(n) + " +0,0 @@\n")
	}
	for rest := content; rest != ""; {
		line, tail, newline := strings.Cut(rest, "\n")
		rest = tail
		body.WriteByte(prefix)
		body.WriteString(line)
		body.WriteByte('\n')
		if !newline {
			body.WriteString("\\ No newline at end of file\n")
		}
	}
	d.Text = body.String()
	return d
}

// Equality is over recorded evidence, not generated diffs or display messages.
// Adapters sort files once and normalize optional values before this comparison.
func codexFileChangesEqual(a, b *FileChangeSet) bool {
	if a.State != b.State || a.ChangesAvailable != b.ChangesAvailable ||
		a.Stdout != b.Stdout || a.Stderr != b.Stderr || len(a.Files) != len(b.Files) ||
		!codexChangeDiagnosticsEqual(a.Diagnostics, b.Diagnostics) {
		return false
	}
	for i, af := range a.Files {
		bf := b.Files[i]
		if af.Path != bf.Path || af.Kind != bf.Kind || af.MovePath != bf.MovePath ||
			af.Content != bf.Content || !codexChangeDiagnosticsEqual(af.Diagnostics, bf.Diagnostics) {
			return false
		}
	}
	return true
}

func codexChangeDiagnosticsEqual(a, b []FileChangeDiagnostic) bool {
	if len(a) != len(b) {
		return false
	}
	for i, diagnostic := range a {
		if diagnostic.Code != b[i].Code || diagnostic.Field != b[i].Field || diagnostic.Value != b[i].Value {
			return false
		}
	}
	return true
}

// Reconcile once before materializing entries. Slot indices stay stable, so
// assistant grouping and physical anchors remain independent of deduplication.
func codexReconcileFileChanges(slots []codexSlot, log *slog.Logger) {
	type firstEvent struct {
		index    int
		original *FileChangeSet
		conflict *FileChangeDiagnostic
	}
	byID := make(map[string]*firstEvent)
	for i := range slots {
		s := &slots[i]
		if s.kind != EntryFileChange || s.fileChange.ID == "" {
			continue
		}
		first := byID[s.fileChange.ID]
		if first == nil {
			byID[s.fileChange.ID] = &firstEvent{index: i, original: s.fileChange}
			continue
		}
		if !codexFileChangesEqual(first.original, s.fileChange) {
			canonical := &slots[first.index]
			if first.conflict == nil {
				// Preserve the original evidence for subsequent comparisons. Only
				// the canonical copy acquires the unconfirmed state/diagnostic.
				merged := *first.original
				merged.State = FileChangeUnconfirmed
				merged.Diagnostics = append(append([]FileChangeDiagnostic(nil), merged.Diagnostics...), FileChangeDiagnostic{
					Code: "identity", Field: "id", Message: "Conflicting records for the same operation; changes are unconfirmed.",
					SourceLines: []int{canonical.lineIndex},
				})
				first.conflict = &merged.Diagnostics[len(merged.Diagnostics)-1]
				canonical.fileChange = &merged
				log.Warn("vault codex decoder: conflicting file change identity",
					"line", s.lineIndex, "first_line", canonical.lineIndex, "category", "item_completed/FileChange", "reason", "identity")
			}
			first.conflict.SourceLines = append(first.conflict.SourceLines, s.lineIndex)
		}
		// The first slot owns the canonical entry, including conflicts. Keep
		// all original records available through the unchanged raw archive.
		s.fileChange = nil
	}
}

// Log at most one bounded warning per recognized event. Tool output, patches,
// paths and offending wire values stay in the model, never in logs.
func codexLogFileChange(log *slog.Logger, s *FileChangeSet, line int) {
	var reason string
	if len(s.Diagnostics) > 0 {
		reason = s.Diagnostics[0].Code
	} else {
		for _, f := range s.Files {
			for _, diagnostic := range f.Diagnostics {
				reason = diagnostic.Code
				break
			}
			if reason != "" {
				break
			}
		}
	}
	if reason != "" {
		log.Warn("vault codex decoder: file change data unavailable or unconfirmed",
			"line", line, "category", "item_completed/FileChange", "reason", reason)
	}
}

var codexUnifiedHunk = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@(?: .*)?$`)

// codexUpdateDiff validates numbered unified hunks in one pass. File headers
// are excluded from the candidate body (the viewer supplies plain sections).
// Original bytes, including CRLF, remain in FileChange.Content for equality.
func codexUpdateDiff(text string) (*Diff, string) {
	const invalid = "Recorded update has malformed unified hunks or inconsistent line counts."
	d := &Diff{}
	var body strings.Builder
	oldLeft, newLeft, hunks := 0, 0, 0
	canAnnotate, oldHeader, newHeader := false, false, false
	for rest := text; rest != ""; {
		line, tail, newline := strings.Cut(rest, "\n")
		rest = tail
		parseLine := strings.TrimSuffix(line, "\r")
		switch {
		case hunks == 0 && strings.HasPrefix(parseLine, "--- ") && !oldHeader:
			oldHeader = true
			continue
		case hunks == 0 && strings.HasPrefix(parseLine, "+++ ") && oldHeader && !newHeader:
			newHeader = true
			continue
		case strings.HasPrefix(parseLine, "@@"):
			if oldLeft != 0 || newLeft != 0 || oldHeader != newHeader {
				return nil, invalid
			}
			match := codexUnifiedHunk.FindStringSubmatch(parseLine)
			if match == nil {
				return nil, invalid
			}
			var ok bool
			oldLeft, ok = codexHunkRange(match[1], match[2])
			if !ok {
				return nil, invalid
			}
			newLeft, ok = codexHunkRange(match[3], match[4])
			if !ok {
				return nil, invalid
			}
			hunks++
			canAnnotate = false
		case parseLine == `\ No newline at end of file`:
			if !canAnnotate {
				return nil, invalid
			}
			canAnnotate = false
		default:
			if hunks == 0 || parseLine == "" {
				return nil, invalid
			}
			switch parseLine[0] {
			case ' ':
				oldLeft--
				newLeft--
			case '-':
				oldLeft--
				d.Removed++
			case '+':
				newLeft--
				d.Added++
			default:
				return nil, invalid
			}
			if oldLeft < 0 || newLeft < 0 {
				return nil, invalid
			}
			canAnnotate = true
		}
		body.WriteString(line)
		if newline {
			body.WriteByte('\n')
		}
	}
	if hunks == 0 || oldLeft != 0 || newLeft != 0 {
		return nil, invalid
	}
	d.Text = body.String()
	return d, ""
}

func codexHunkRange(startText, countText string) (int, bool) {
	start, err := strconv.Atoi(startText)
	if err != nil {
		return 0, false
	}
	count := 1
	if countText != "" {
		count, err = strconv.Atoi(countText)
		if err != nil {
			return 0, false
		}
	}
	return count, start > 0 || count == 0
}
