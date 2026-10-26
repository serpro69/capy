//go:build linux || (darwin && cgo)

package execution

import (
	"context"
	"os/exec"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
)

func newSupervisorLaunch() supervisorLaunch { return newLeaseLauncher().launchSupervisor }

func (launcher *leaseLauncher) launchSupervisor(ctx context.Context, executable string, start Startup, key string, refs *runstore.LeaseRefs) (*Supervisor, error) {
	if start.CleanupScope != string(supervisorCleanupScope()) {
		return nil, &runstore.Error{Code: "cleanup_unverified", Message: "supervisor requires its backend cleanup scope"}
	}
	cmd := exec.Command(executable, SupervisorCommand)
	p, err := launcher.launch(ctx, cmd, start, key, refs)
	if p == nil {
		return nil, err
	}
	return &Supervisor{cmd: cmd, input: p.input, events: p.events, exchangeID: p.start.ExchangeID,
		scope: executor.CleanupScope(p.start.CleanupScope), join: func() error { return p.Wait(context.Background()) },
		knownNotSpawned: p.knownNotSpawned, reaped: p.reaped}, err
}
