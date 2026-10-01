package platform

import (
	"fmt"
	"os"
	"testing"
)

// Setup loads global configuration and can chmod an absolute key-file target.
// Keep every platform test away from the developer's real configuration;
// tests of global settings can override this with their own temporary fixtures.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "capy-platform-config-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "isolating platform test configuration:", err)
		os.Exit(1)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		fmt.Fprintln(os.Stderr, "setting platform test configuration:", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, "removing platform test configuration:", err)
		code = 1
	}
	os.Exit(code)
}
