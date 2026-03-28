package hook

import "github.com/serpro69/capy/internal/adapter"

func handlePreCompact(_ []byte, _ adapter.HookAdapter) ([]byte, error) {
	return nil, nil // STUB: Future resume snapshot
}
