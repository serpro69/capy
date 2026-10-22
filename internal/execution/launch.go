package execution

import (
	"context"
	"os"

	"github.com/serpro69/capy/internal/runstore"
)

// Launch accepts only references originating in runstore's successful lease
// acquisitions. The callback pins their exact descriptions through helper exec;
// inherited references then protect the helper's lifetime independently.
func Launch(ctx context.Context, executable string, start Startup, key string, refs *runstore.LeaseRefs) (*Supervisor, error) {
	var helper *Supervisor
	err := refs.WithFiles(start.StorePath, start.Generation, func(owner, lifecycle *os.File) error {
		var err error
		helper, err = launch(ctx, executable, start, key, Leases{Owner: owner, Lifecycle: lifecycle})
		return err
	})
	return helper, err
}
