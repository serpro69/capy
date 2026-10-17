package config

import (
	"fmt"
	"math"
	"path/filepath"
	"time"
)

type runsOverlay struct {
	WaitMS           *int   `toml:"wait_ms"`
	MaxResponseBytes *int   `toml:"max_response_bytes"`
	MaxActive        *int   `toml:"max_active"`
	MaxPending       *int   `toml:"max_pending"`
	TTLHours         *int   `toml:"ttl_hours"`
	MaxRunBytes      *int64 `toml:"max_run_bytes"`
	MaxRetainedBytes *int64 `toml:"max_retained_bytes"`
}

func mergeRuns(dst *RunsConfig, src runsOverlay) error {
	for _, f := range []struct {
		name      string
		value     *int
		target    *int
		low, high int
	}{
		{"wait_ms", src.WaitMS, &dst.WaitMS, 0, 30000},
		{"max_response_bytes", src.MaxResponseBytes, &dst.MaxResponseBytes, 2048, 65536},
		{"max_active", src.MaxActive, &dst.MaxActive, 1, 8},
		{"max_pending", src.MaxPending, &dst.MaxPending, 1, 256},
		{"ttl_hours", src.TTLHours, &dst.TTLHours, 1, int(math.MaxInt64 / int64(time.Hour))},
	} {
		if f.value == nil {
			continue
		}
		if *f.value < f.low || *f.value > f.high {
			return fmt.Errorf("[runs] %s must be between %d and %d", f.name, f.low, f.high)
		}
		*f.target = *f.value
	}
	for _, f := range []struct {
		name   string
		value  *int64
		target *int64
	}{
		{"max_run_bytes", src.MaxRunBytes, &dst.MaxRunBytes}, {"max_retained_bytes", src.MaxRetainedBytes, &dst.MaxRetainedBytes},
	} {
		if f.value == nil {
			continue
		}
		if *f.value < 1 {
			return fmt.Errorf("[runs] %s must be positive", f.name)
		}
		*f.target = *f.value
	}
	return nil
}

func validateRuns(r RunsConfig) error {
	copy := r
	err := mergeRuns(&copy, runsOverlay{&r.WaitMS, &r.MaxResponseBytes, &r.MaxActive, &r.MaxPending, &r.TTLHours, &r.MaxRunBytes, &r.MaxRetainedBytes})
	if err != nil {
		return err
	}
	if r.MaxRunBytes > r.MaxRetainedBytes {
		return fmt.Errorf("[runs] max_run_bytes must not exceed max_retained_bytes")
	}
	return nil
}

// ResolveRunDBPath preserves the selected physical installation/worktree. Cwd
// overrides and the knowledge store's path/main-worktree sharing never select it.
func ResolveRunDBPath(owner string) (string, error) {
	abs, err := filepath.Abs(owner)
	if err != nil {
		return "", err
	}
	physical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Join(physical, ".capy", "runs", "runs.db"), nil
}
