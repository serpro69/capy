//go:build !linux

package execution

import (
	"context"

	"github.com/serpro69/capy/internal/runstore"
)

func newSupervisorLaunch() supervisorLaunch {
	return func(context.Context, string, Startup, string, *runstore.LeaseRefs) (*Supervisor, error) {
		return nil, Supported()
	}
}
