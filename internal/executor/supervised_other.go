//go:build !linux

package executor

import (
	"context"
	"errors"
)

func PrepareSupervisor() error { return errors.New("run supervision is supported only on Linux") }
func checkSupervisor() error   { return PrepareSupervisor() }
func runStreamPhase(context.Context, string, []string, string, string, ExecRequest, string, *eventWriter) (StreamResult, error) {
	return StreamResult{Retired: true}, PrepareSupervisor()
}
