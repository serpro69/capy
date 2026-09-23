package vault

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexCanaryPrompts_IndependentReconciliation(t *testing.T) {
	const reply = `<send_user_message_question_reply>[{"answer":"Use the existing scope"}]</send_user_message_question_reply>`
	for _, mode := range []string{"legacy", "paginated"} {
		t.Run(mode, func(t *testing.T) {
			response := func(text string) map[string]any {
				return map[string]any{"type": "response_item", "payload": map[string]any{
					"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": text}},
				}}
			}
			event := func(text string) map[string]any {
				payload := map[string]any{"type": "user_message", "message": text}
				if mode == "paginated" {
					payload = map[string]any{"type": "item_completed", "item": map[string]any{
						"type": "UserMessage", "content": []map[string]any{{"type": "input_text", "text": text}},
					}}
				}
				return map[string]any{"type": "event_msg", "payload": payload}
			}
			for _, tc := range []struct {
				name      string
				rows      []map[string]any
				wantError bool
			}{
				{"tagged reply", []map[string]any{response(reply), event(reply)}, false},
				{"ordinary prompt", []map[string]any{response("continue"), event("continue")}, false},
				{"noise stays noise", []map[string]any{response("<environment_context>injected</environment_context>"), response("# AGENTS.md instructions for /p")}, false},
				{"missing response", []map[string]any{event(reply)}, true},
				{"missing tagged event", []map[string]any{response("continue"), event("continue"), response(reply)}, true},
				{"event-free tagged reply", []map[string]any{response(reply)}, true},
				{"different response", []map[string]any{response("something else"), event(reply)}, true},
				{"duplicate response", []map[string]any{response(reply), response(reply), event(reply)}, true},
				{"duplicate event", []map[string]any{response(reply), event(reply), event(reply)}, true},
				{"equal counts wrong multiplicity", []map[string]any{response("one"), response("two"), event("one"), event("one")}, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					tally, err := tallyCodexRaw("fixture.jsonl", jsonlBytes(t, tc.rows...), nil)
					require.NoError(t, err)
					err = reconcileCodexCanaryPrompts(tally)
					if tc.wantError {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
						assert.Equal(t, tally.eventTexts, tally.filteredTexts)
					}
				})
			}
		})
	}
}
