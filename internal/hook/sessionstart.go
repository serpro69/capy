package hook

import "github.com/serpro69/capy/internal/adapter"

func handleSessionStart(_ []byte, a adapter.HookAdapter) ([]byte, error) {
	if a.Platform() == adapter.Codex {
		return a.FormatSessionStart(CodexRoutingBlock())
	}
	return a.FormatSessionStart(RoutingBlock())
}
