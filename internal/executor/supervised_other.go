//go:build !linux && (!darwin || !cgo)

package executor

import (
	"context"
	"errors"
)

func PrepareSupervisor() error { return errors.New("run supervision is supported only on Linux") }
func checkSupervisor() error   { return PrepareSupervisor() }
func runStreamPhase(context.Context, string, []string, string, string, ExecRequest, string, *eventWriter) (StreamResult, error) {
	return StreamResult{}, PrepareSupervisor()
}

// No backend scope is selected on unsupported production platforms.
func initialStreamCleanup() CleanupEvidence { return CleanupEvidence{} }
