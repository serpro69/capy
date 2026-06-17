package hook

import "github.com/serpro69/capy/internal/adapter"

// handlePreCompact is intentionally a no-op. PreCompact archival was investigated
// under Task 0 / V2.0 and dropped: /compact is append-only to the session JSONL,
// so no file content is lost, and the existing server-startup sweep already
// archives the full verbatim transcript. The investigation — payload shape, the
// append-only finding, the decision, and the conditions to revisit it — is in
// docs/wip/vault/v2/precompact-investigation.md. The debug instrumentation that
// produced it is preserved in this branch's git history for easy revival.
func handlePreCompact(_ []byte, _ adapter.HookAdapter) ([]byte, error) {
	return nil, nil
}
