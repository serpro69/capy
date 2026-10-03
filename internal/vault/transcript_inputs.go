package vault

import "strings"

// executableOutputAliases is viewer-only and keyed by response call ID, never
// by a nested edit event's identity. Build it before composition so results can
// precede their calls without falling back to a large input-derived summary.
func executableOutputAliases(entries []Entry) map[string]string {
	aliases := make(map[string]string)
	for _, e := range entries {
		if e.Kind != EntryAssistant {
			continue
		}
		for _, p := range e.Parts {
			if p.Call != nil && p.Call.ID != "" && collapsedExecutableInput(p.Call) {
				aliases[p.Call.ID] = "exec · output"
			}
		}
	}
	return aliases
}

func collapsedExecutableInput(call *ToolCall) bool {
	return call.Launch == nil && call.CodeText != "" && overCollapseThreshold(call.CodeText)
}

// assistantBodyAndDetails keeps one body and appends input/launch markers in
// their original call order. Launch-only entries preserve their existing body
// and source-line tie behavior; only an input owner becomes a source anchor.
func assistantBodyAndDetails(parts []Part) (body string, details []TranscriptMessage, sourceAnchor bool) {
	var lines []string
	for _, p := range parts {
		switch {
		case p.Call != nil && p.Call.Launch != nil:
			l := p.Call.Launch
			details = append(details, TranscriptMessage{
				Role: RoleSubagent, Body: l.Label,
				ChildUUID: l.ChildUUID, Openable: l.ChildUUID != "",
			})
		case p.Call != nil && collapsedExecutableInput(p.Call):
			lines = append(lines, "→ exec · input")
			details = append(details, TranscriptMessage{
				Role: RoleTool, Body: p.Call.CodeText, Heading: "Tool input",
				Collapsed: true, ToolSummary: "exec · input",
			})
			sourceAnchor = true
		case p.Call != nil:
			if p.Call.Summary != "" {
				lines = append(lines, "→ "+p.Call.Summary)
			}
		case p.Text != "":
			lines = append(lines, p.Text)
		}
	}
	return strings.Join(lines, "\n"), details, sourceAnchor
}
