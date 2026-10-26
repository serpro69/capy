//go:build !linux && (!darwin || !cgo)

package execution

import (
	"context"
	"errors"

	"github.com/serpro69/capy/internal/executor"
)

func ServeInherited(context.Context, OpenRun) error {
	return errors.New("run supervision requires Linux or macOS 26 on Apple Silicon with CGO")
}

func supervisorCleanupScope() executor.CleanupScope { return "" }
