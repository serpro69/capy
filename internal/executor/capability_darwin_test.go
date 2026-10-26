//go:build darwin && cgo

package executor

import "testing"

func TestDarwinObservationSupportMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, arch, version string
		accepted            bool
	}{
		{"floor", "arm64", "26.0", true},
		{"patch", "arm64", "26.6.2", true},
		{"older major", "arm64", "15.7", false},
		{"future major", "arm64", "27.0", false},
		{"intel", "amd64", "26.0", false},
		{"unknown architecture", "", "26.0", false},
		{"unknown version", "arm64", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := darwinObservationMatrix(tc.arch, tc.version); (err == nil) != tc.accepted {
				t.Fatalf("support for %s/%s: %v, want accepted=%t", tc.arch, tc.version, err, tc.accepted)
			}
		})
	}
}
