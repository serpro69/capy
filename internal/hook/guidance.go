package hook

import (
	"sync"

	"github.com/serpro69/capy/internal/adapter"
)

// guidanceThrottle tracks which guidance types have been shown this session.
// In-memory only — sufficient since Go hooks run as the same long-lived process
// (unlike Node which spawns per-invocation).
var (
	guidanceShown   = make(map[string]struct{})
	guidanceShownMu sync.Mutex
)

// guidanceOnce returns guidance context the first time a type is requested,
// and nil on subsequent calls. This ensures each advisory is shown at most
// once per session.
func guidanceOnce(guidanceType, content string, a adapter.HookAdapter) ([]byte, error) {
	guidanceShownMu.Lock()
	defer guidanceShownMu.Unlock()

	if _, shown := guidanceShown[guidanceType]; shown {
		return nil, nil
	}
	guidanceShown[guidanceType] = struct{}{}

	return a.FormatAllow(content)
}

// ResetGuidanceThrottle clears the guidance throttle. Used in tests.
func ResetGuidanceThrottle() {
	guidanceShownMu.Lock()
	defer guidanceShownMu.Unlock()
	guidanceShown = make(map[string]struct{})
}
