package vault

import (
	"fmt"
	"path"
	"strings"
)

// viewerFileChangeMessage builds one existing tool detail per recorded event.
// Only completed operations expose candidate hunks or line counts.
func viewerFileChangeMessage(e Entry, cwd string) TranscriptMessage {
	s := e.FileChange
	state := s.State
	if state == "" {
		state = FileChangeUnconfirmed
	}
	m := TranscriptMessage{Role: RoleTool, SourceLine: e.LineIndex, Collapsed: true,
		Heading: "File changes · " + string(state)}
	var body strings.Builder
	complete := state == FileChangeCompleted
	switch state {
	case FileChangeCompleted:
		body.WriteString("Patch reported completed.\n")
	case FileChangeFailed:
		m.ToolSummary = "Patch failed"
		body.WriteString("Patch reported failed; partial filesystem changes may have occurred.\n")
	case FileChangeDeclined:
		m.ToolSummary = "Patch declined"
		body.WriteString("Patch reported declined.\n")
	default:
		m.ToolSummary = "Patch unconfirmed"
		body.WriteString("Patch completion is unconfirmed.\n")
	}
	writeChangeDiagnostics(&body, s.Diagnostics)
	added, removed, available := 0, 0, s.ChangesAvailable
	for _, file := range s.Files {
		body.WriteByte('\n')
		body.WriteString(fileChangeSection(file, cwd))
		switch {
		case !complete:
			body.WriteString(" (reported)\n")
		case file.Diff == nil:
			available = false
			body.WriteString(" (diff unavailable)\n")
		case file.Diff != nil:
			fmt.Fprintf(&body, " (+%d −%d)\n", file.Diff.Added, file.Diff.Removed)
			body.WriteString(file.Diff.Text)
			if !strings.HasSuffix(file.Diff.Text, "\n") {
				body.WriteByte('\n')
			}
			added += file.Diff.Added
			removed += file.Diff.Removed
			m.Diff = true
		}
		writeChangeDiagnostics(&body, file.Diagnostics)
	}
	if !s.ChangesAvailable && len(s.Diagnostics) == 0 {
		body.WriteString("Recorded changes data is unavailable.\n")
	}
	if complete {
		noun := "files"
		if len(s.Files) == 1 {
			noun = "file"
		}
		switch {
		case !s.ChangesAvailable:
			m.ToolSummary = "Patch completed · diff unavailable"
		case len(s.Files) == 0:
			m.ToolSummary = "Patch completed · no file changes recorded"
		case !available:
			m.ToolSummary = fmt.Sprintf("%d %s changed · diff incomplete", len(s.Files), noun)
		default:
			m.ToolSummary = fmt.Sprintf("%d %s changed (+%d −%d)", len(s.Files), noun, added, removed)
		}
	}
	for _, output := range []struct{ label, text string }{{"stdout", s.Stdout}, {"stderr", s.Stderr}} {
		if output.text != "" {
			body.WriteString("\n" + output.label + ":\n")
			body.WriteString(output.text)
			if !strings.HasSuffix(output.text, "\n") {
				body.WriteByte('\n')
			}
		}
	}
	m.Body = body.String()
	return m
}

func writeChangeDiagnostics(body *strings.Builder, diagnostics []FileChangeDiagnostic) {
	for _, diagnostic := range diagnostics {
		body.WriteString(diagnostic.Message)
		body.WriteByte('\n')
	}
}

func fileChangeSection(file FileChange, cwd string) string {
	display := fileChangeDisplayPath(file.Path, cwd)
	if file.MovePath != "" {
		return "*** Move File: " + display + " → " + fileChangeDisplayPath(file.MovePath, cwd)
	}
	switch file.Kind {
	case "add":
		return "*** Add File: " + display
	case "delete":
		return "*** Delete File: " + display
	case "update":
		return "*** Update File: " + display
	default:
		return "*** File: " + display
	}
}

// Shorten only lexical POSIX descendants of the archived cwd. Backslashes,
// drive paths and UNC paths stay verbatim regardless of the viewer's host OS.
func fileChangeDisplayPath(original, cwd string) string {
	if !strings.HasPrefix(original, "/") || !strings.HasPrefix(cwd, "/") ||
		strings.HasPrefix(original, "//") || strings.HasPrefix(cwd, "//") ||
		strings.ContainsAny(original+cwd, `\`) {
		return original
	}
	prefix := strings.TrimSuffix(path.Clean(cwd), "/") + "/"
	clean := path.Clean(original)
	if strings.HasPrefix(clean, prefix) && len(clean) > len(prefix) {
		return strings.TrimPrefix(clean, prefix)
	}
	return original
}
