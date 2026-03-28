package hook

import "github.com/serpro69/capy/internal/adapter"

func handleSessionStart(_ []byte, a adapter.HookAdapter) ([]byte, error) {
	return a.FormatSessionStart(RoutingBlock())
}
