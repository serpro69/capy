package server

import (
	"fmt"

	"github.com/serpro69/capy/internal/executor"
)

// executionCwd validates supplied values before execution or indexing. Resolve
// once at the tool boundary so every batch worker gets the same absolute cwd.
func (s *Server) executionCwd(args map[string]any) (string, error) {
	var cwd string
	if value, present := args["cwd"]; present {
		var ok bool
		cwd, ok = value.(string)
		if !ok || cwd == "" {
			return "", fmt.Errorf("invalid parameter %q: expected a nonempty directory string", "cwd")
		}
	}
	return executor.ResolveCwd(s.projectDir, cwd)
}
