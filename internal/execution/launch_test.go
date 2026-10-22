package execution

import (
	"context"
	"testing"

	"github.com/serpro69/capy/internal/runstore"
)

func TestLaunchRejectsUnacquiredReferences(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs *runstore.LeaseRefs
	}{
		{"nil", nil},
		{"zero", &runstore.LeaseRefs{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			helper, err := Launch(context.Background(), "must-not-spawn", Startup{}, "synthetic-key", tc.refs)
			if err == nil || helper != nil {
				t.Fatalf("unacquired launch: helper=%v err=%v", helper, err)
			}
		})
	}
}
