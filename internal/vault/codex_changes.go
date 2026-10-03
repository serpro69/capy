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
	return FileChangeDiagnostic{Code: code, Field: field, Value: string(raw), Message: message}
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
	if f.Kind != "update" {
		// Task 3 adds archived-content conversion for adds/deletes. Retain the
		// source now, and show the operation as unavailable instead of omitting it.
		f.Content, _ = asJSONString(wire.Content)
		f.Diagnostics = append(f.Diagnostics, changeDiagnostic("operation", "type", wire.Type,
			"Diff conversion for this file operation is unavailable."))
		return f
	}
	text, ok := asJSONString(wire.UnifiedDiff)
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
			// Task 3 completes move conversion; do not show a move as an update.
			f.Diagnostics = append(f.Diagnostics, changeDiagnostic("move", "move_path", nil,
				"Diff conversion for moves is unavailable."))
		}
	}
	if len(f.Diagnostics) > 0 {
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

// Log at most one bounded warning per recognized event. Tool output, patches,
// paths and offending wire values stay in the model, never in logs.
func codexLogFileChange(log *slog.Logger, s *FileChangeSet, line int) {
	var reason string
	if len(s.Diagnostics) > 0 {
		reason = s.Diagnostics[0].Code
	} else {
		for _, f := range s.Files {
			for _, diagnostic := range f.Diagnostics {
				// These are recognized operations intentionally awaiting Task 3,
				// not malformed archives or format drift.
				if diagnostic.Code == "move" || diagnostic.Code == "operation" && (f.Kind == "add" || f.Kind == "delete") {
					continue
				}
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
