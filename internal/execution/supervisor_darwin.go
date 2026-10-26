//go:build darwin && cgo

package execution

import (
	"context"

	"github.com/serpro69/capy/internal/executor"
)

func supervisorCleanupScope() executor.CleanupScope { return executor.CleanupDarwinObservation }

// ServeInherited is reachable only through the private helper path. Public
// admission remains gated by Supported until native integration is accepted.
func ServeInherited(ctx context.Context, open OpenRun) error {
	return serveInherited(ctx, open, supervisorCleanupScope())
}
