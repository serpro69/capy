package hook

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPreCompact_NoOp locks the contract that the PreCompact handler ships no
// behavior: archival was investigated (Task 0 / V2.0) and dropped — see
// docs/wip/vault/v2/precompact-investigation.md.
func TestPreCompact_NoOp(t *testing.T) {
	output, err := handlePreCompact([]byte(`{"trigger":"manual","transcript_path":"/x.jsonl"}`), &testAdapter{})
	require.NoError(t, err)
	assert.Nil(t, output, "PreCompact must produce no output")
}
