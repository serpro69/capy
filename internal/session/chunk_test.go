package session

import (
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/store"
)

func testSession() *ParsedSession {
	return &ParsedSession{
		SessionID: "test-session-id",
		StartTime: time.Date(2026, 4, 5, 12, 6, 26, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "How do I set up the project?", AssistantText: "Run `make setup` to install dependencies."},
			{HumanText: "What about testing?", AssistantText: "Use `make test` with the fts5 tag.", ToolNames: []string{"Read"}},
			{HumanText: "Can you fix the bug?", AssistantText: "Fixed the regex in the parser module."},
			{HumanText: "How do I deploy?", AssistantText: "Run `make deploy` targeting production."},
			{HumanText: "Any cleanup needed?", AssistantText: "I cleaned up the temp files and stale caches."},
			{HumanText: "Thanks for the help", AssistantText: "You're welcome! Let me know if anything else comes up."},
		},
		TotalAssistantChars: 300,
	}
}

func TestBuildTranscript_Basic(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "Hello", AssistantText: "Hi there"},
			{HumanText: "Fix bug", AssistantText: "Done", ToolNames: []string{"Edit"}, ToolMeta: []string{"[Edit: main.go]"}},
		},
	}

	tr := BuildTranscript(s)
	got := tr.Text

	if !strings.Contains(got, "Human: Hello") {
		t.Errorf("missing human text, got:\n%s", got)
	}
	if !strings.Contains(got, "Assistant: Hi there") {
		t.Errorf("missing assistant text, got:\n%s", got)
	}
	if !strings.Contains(got, "[Edit: main.go]") {
		t.Errorf("missing enriched metadata line, got:\n%s", got)
	}
	// First turn should NOT have metadata lines.
	lines := strings.Split(got, "\n")
	for i, line := range lines {
		if strings.Contains(line, "Assistant: Hi there") {
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "[") {
				t.Error("first turn should not have metadata lines")
			}
		}
	}

	// Verify offsets cover the full text.
	if len(tr.Offsets) != 2 {
		t.Fatalf("expected 2 offsets, got %d", len(tr.Offsets))
	}
	if tr.Offsets[0].Start != 0 {
		t.Errorf("first offset start should be 0, got %d", tr.Offsets[0].Start)
	}
	if tr.Offsets[len(tr.Offsets)-1].End != len(got) {
		t.Errorf("last offset end should equal text length %d, got %d", len(got), tr.Offsets[len(tr.Offsets)-1].End)
	}
}

func TestBuildTranscript_AwaySummary(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "Start", AssistantText: "Starting now"},
			{HumanText: "", AssistantText: "We worked on the parser"},
			{HumanText: "Continue", AssistantText: "Continuing"},
		},
	}

	tr := BuildTranscript(s)

	if !strings.Contains(tr.Text, "[Session summary: We worked on the parser]") {
		t.Errorf("missing session summary, got:\n%s", tr.Text)
	}
}

func TestBuildTranscript_SubagentGrouping(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "Main question", AssistantText: "Main answer"},
			{HumanText: "Sub q1", AssistantText: "Sub a1", IsSubagent: true, SubagentType: "Explore", SubagentDesc: "Find endpoints"},
			{HumanText: "Sub q2", AssistantText: "Sub a2", IsSubagent: true, SubagentType: "Explore", SubagentDesc: "Find endpoints"},
			{HumanText: "Follow up", AssistantText: "Follow up answer"},
		},
	}

	tr := BuildTranscript(s)
	got := tr.Text

	if strings.Count(got, "--- Subagent: Explore") != 1 {
		t.Errorf("expected 1 subagent opening, got:\n%s", got)
	}
	if strings.Count(got, "--- End subagent ---") != 1 {
		t.Errorf("expected 1 subagent closing, got:\n%s", got)
	}
	if !strings.Contains(got, `"Find endpoints"`) {
		t.Errorf("missing subagent description, got:\n%s", got)
	}

	// Verify offsets: each subagent turn should have valid offsets.
	for i, off := range tr.Offsets {
		if off.End < off.Start {
			t.Errorf("offset %d: end (%d) < start (%d)", i, off.End, off.Start)
		}
		slice := got[off.Start:off.End]
		if i == 1 {
			// First subagent turn includes the opening delimiter.
			if !strings.Contains(slice, "--- Subagent:") {
				t.Errorf("first subagent turn should include opening delimiter, got:\n%s", slice)
			}
		}
		if i == 2 {
			// Last subagent turn includes the closing delimiter.
			if !strings.Contains(slice, "--- End subagent ---") {
				t.Errorf("last subagent turn should include closing delimiter, got:\n%s", slice)
			}
		}
	}
}

func TestChunkSession_SingleChunk(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 6, 26, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "Hello", AssistantText: "Hi"},
			{HumanText: "Bye", AssistantText: "See ya"},
		},
		TotalAssistantChars: 8,
	}

	tr := BuildTranscript(s)
	chunks := ChunkSession(s, tr, 0)

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for small session, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Title, "Session 2026-04-05T12:06:26Z") {
		t.Errorf("unexpected title: %s", chunks[0].Title)
	}
	if !strings.Contains(chunks[0].Title, "Turns 1-2") {
		t.Errorf("unexpected title: %s", chunks[0].Title)
	}
}

func TestChunkSession_SlidingWindow(t *testing.T) {
	s := testSession()
	tr := BuildTranscript(s)

	// Use a small maxBytes to force multiple chunks from the 6-turn session.
	chunks := ChunkSession(s, tr, 200)

	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for 6-turn session, got %d", len(chunks))
	}

	for i, c := range chunks {
		if !strings.Contains(c.Title, "Session 2026-04-05T12:06:26Z") {
			t.Errorf("chunk %d: missing session timestamp in title: %s", i, c.Title)
		}
		if !strings.Contains(c.Title, "Turns") {
			t.Errorf("chunk %d: missing turns in title: %s", i, c.Title)
		}
		if c.Content == "" {
			t.Errorf("chunk %d: empty content", i)
		}
	}
}

func TestChunkSession_Overlap(t *testing.T) {
	s := testSession()
	tr := BuildTranscript(s)
	chunks := ChunkSession(s, tr, 200)

	if len(chunks) < 2 {
		t.Skipf("need at least 2 chunks for overlap test, got %d", len(chunks))
	}

	// With window=4, step=3: first window = Turns 1-4, second = Turns 4-6.
	// Turn 4 ("How do I deploy?") should appear in both windows.
	overlapText := "How do I deploy?"
	var titlesContaining []string
	for _, c := range chunks {
		if strings.Contains(c.Content, overlapText) {
			titlesContaining = append(titlesContaining, c.Title)
		}
	}

	// The overlap text should appear in chunks from at least 2 different windows.
	uniqueTitles := make(map[string]bool)
	for _, t := range titlesContaining {
		uniqueTitles[t] = true
	}
	if len(uniqueTitles) < 2 {
		t.Errorf("overlap text %q should appear in chunks from 2+ windows, found in %d unique title(s): %v",
			overlapText, len(uniqueTitles), titlesContaining)
	}
}

func TestChunkSession_ToolNamesInTitle(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "Read file", AssistantText: "Here it is", ToolNames: []string{"Read", "Edit"}},
			{HumanText: "Search", AssistantText: "Found it", ToolNames: []string{"mcp__capy__capy_search"}},
		},
		TotalAssistantChars: 20,
	}

	tr := BuildTranscript(s)
	chunks := ChunkSession(s, tr, 0)

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	title := chunks[0].Title
	if !strings.Contains(title, "Tools: Read, Edit, mcp__capy__capy_search") {
		t.Errorf("tool names not in title: %s", title)
	}
}

func TestChunkSession_SubagentInTitle(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "Main", AssistantText: "OK"},
			{HumanText: "Sub", AssistantText: "Found", IsSubagent: true, SubagentType: "Explore", SubagentDesc: "Find things"},
		},
		TotalAssistantChars: 10,
	}

	tr := BuildTranscript(s)
	chunks := ChunkSession(s, tr, 0)

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	if !strings.Contains(chunks[0].Title, "Subagent: Explore") {
		t.Errorf("missing subagent in title: %s", chunks[0].Title)
	}
}

func TestChunkSession_OversizedSplit(t *testing.T) {
	// Use paragraph-separated content so SplitOversized can find break points.
	para := strings.Repeat("This is a paragraph of text. ", 20)
	longText := strings.Join([]string{para, para, para, para, para, para}, "\n\n")
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "Generate a lot", AssistantText: longText},
			{HumanText: "More please", AssistantText: longText},
		},
		TotalAssistantChars: len(longText) * 2,
	}

	tr := BuildTranscript(s)
	chunks := ChunkSession(s, tr, store.MaxChunkBytes)

	if len(chunks) < 2 {
		t.Fatalf("oversized content should produce multiple chunks, got %d", len(chunks))
	}

	for i, c := range chunks {
		if len(c.Content) > store.MaxChunkBytes*2 {
			t.Errorf("chunk %d exceeds 2x max: %d bytes", i, len(c.Content))
		}
	}
}

func TestChunkSession_Empty(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: nil,
	}

	tr := BuildTranscript(s)
	chunks := ChunkSession(s, tr, 0)

	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty session, got %d", len(chunks))
	}
}

func TestBuildChunkTitle_Format(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 6, 26, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "A", AssistantText: "B", ToolNames: []string{"Read"}},
			{HumanText: "C", AssistantText: "D"},
			{HumanText: "E", AssistantText: "F", ToolNames: []string{"Edit", "Read"}},
		},
	}

	title := buildChunkTitle(s, 0, 2)

	if !strings.HasPrefix(title, "Session 2026-04-05T12:06:26Z | Turns 1-3") {
		t.Errorf("unexpected title prefix: %s", title)
	}
	if !strings.Contains(title, "Tools: Read, Edit") {
		t.Errorf("tool names wrong: %s", title)
	}
}

func TestBuildChunkTitle_PALToolsSeparated(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "A", AssistantText: "B", ToolNames: []string{"mcp__pal__chat", "Read"}},
			{HumanText: "C", AssistantText: "D", ToolNames: []string{"Edit"}},
		},
	}

	title := buildChunkTitle(s, 0, 1)

	if !strings.Contains(title, "| PAL: chat") {
		t.Errorf("PAL label missing from title: %s", title)
	}
	if !strings.Contains(title, "| Tools: Read, Edit") {
		t.Errorf("non-PAL tools wrong in title: %s", title)
	}
	if strings.Contains(title, "mcp__pal__") {
		t.Errorf("raw mcp__pal__ prefix should not appear in title: %s", title)
	}
}

func TestBuildChunkTitle_MultiplePALTools(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "A", AssistantText: "B", ToolNames: []string{"mcp__pal__chat", "mcp__pal__thinkdeep"}},
		},
	}

	title := buildChunkTitle(s, 0, 0)

	if !strings.Contains(title, "| PAL: chat, thinkdeep") {
		t.Errorf("multiple PAL tools wrong in title: %s", title)
	}
	if strings.Contains(title, "| Tools:") {
		t.Errorf("no Tools label expected when only PAL tools present: %s", title)
	}
}

func TestBuildChunkTitle_DeduplicatesTools(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "A", AssistantText: "B", ToolNames: []string{"Read"}},
			{HumanText: "C", AssistantText: "D", ToolNames: []string{"Read", "Edit"}},
		},
	}

	title := buildChunkTitle(s, 0, 1)

	readCount := strings.Count(title, "Read")
	if readCount != 1 {
		t.Errorf("Read should appear once, appeared %d times: %s", readCount, title)
	}
}

func TestBuildChunkTitle_SubagentTypesSorted(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "A", AssistantText: "B", IsSubagent: true, SubagentType: "Plan"},
			{HumanText: "C", AssistantText: "D", IsSubagent: true, SubagentType: "Explore"},
		},
	}

	title := buildChunkTitle(s, 0, 1)

	if !strings.Contains(title, "Subagent: Explore, Plan") {
		t.Errorf("subagent types should be sorted: %s", title)
	}
}
