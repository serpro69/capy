//go:build darwin && cgo

package execution

import (
	"context"

	"github.com/serpro69/capy/internal/executor"
)

func supervisorCleanupScope() executor.CleanupScope { return executor.CleanupDarwinObservation }

// ServeInherited handles the private helper path after public admission checks
// the same verified host matrix through Supported.
func ServeInherited(ctx context.Context, open OpenRun) error {
	return serveInherited(ctx, open, supervisorCleanupScope())
}
