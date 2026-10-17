package server

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/serpro69/capy/internal/execution"
)

func TestMain(m *testing.M) {
	// Exercise the production supervisor/backend from the same test executable;
	// CLI transport fixtures separately reexecute the real compiled capy binary.
	if len(os.Args) == 2 && os.Args[1] == execution.SupervisorCommand {
		if err := execution.ServeInherited(context.Background(), execution.OpenStoredRun); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
