//go:build !linux && (!darwin || !cgo)

package execution

import (
	"context"
	"errors"

	"github.com/serpro69/capy/internal/executor"
)

func ServeInherited(context.Context, OpenRun) error {
	return errors.New("run supervision is supported only on Linux")
}

func supervisorCleanupScope() executor.CleanupScope { return "" }
