//go:build !linux && (!darwin || !cgo)

package executor

import (
	"context"
	"errors"
)

func PrepareSupervisor() error {
	return errors.New("run supervision requires Linux or macOS 26 on Apple Silicon with CGO")
}
func checkSupervisor() error { return PrepareSupervisor() }
func runStreamPhase(context.Context, string, []string, string, string, ExecRequest, string, *eventWriter) (StreamResult, error) {
	return StreamResult{}, PrepareSupervisor()
}

// No backend scope is selected on unsupported production platforms.
func initialStreamCleanup() CleanupEvidence { return CleanupEvidence{} }
