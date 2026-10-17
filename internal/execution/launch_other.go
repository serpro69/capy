//go:build !linux

package execution

import "context"

type Supervisor struct{}

func Launch(context.Context, string, Startup, string, Leases) (*Supervisor, error) {
	return nil, Supported()
}
func (*Supervisor) Cancel() error          { return Supported() }
func (*Supervisor) Wait() (Outcome, error) { return Outcome{}, Supported() }
