package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Load reads configuration with three-level precedence (lowest to highest):
//  1. XDG config (~/.config/capy/config.toml)
//  2. Project .capy/config.toml
//  3. Project .capy.toml
//
// Missing files are silently skipped. Malformed TOML returns an error.
func Load(projectDir string) (*Config, error) {
	cfg := DefaultConfig()

	xdg := xdgConfigPath()
	paths := []string{
		filepath.Join(xdg, "capy", "config.toml"),
		filepath.Join(projectDir, ".capy", "config.toml"),
		filepath.Join(projectDir, ".capy.toml"),
	}

	for _, p := range paths {
		if err := loadAndMerge(cfg, p); err != nil {
			return nil, fmt.Errorf("loading %s: %w", p, err)
		}
	}

	if err := validate(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validate enforces invariants on the merged configuration.
func validate(cfg *Config) error {
	if cfg.Executor.Timeout < 1 || int64(cfg.Executor.Timeout) > math.MaxInt64/int64(time.Second) {
		return fmt.Errorf("[executor] timeout must be positive integral seconds within duration range")
	}
	if err := validateRuns(cfg.Runs); err != nil {
		return err
	}
	if cfg.Vault.MinSessionBytes < 0 {
		return fmt.Errorf("[vault] min_session_bytes must be >= 0 (0 disables size filtering)")
	}
	if cfg.Store.Cleanup.EphemeralTTLHours < 1 {
		return fmt.Errorf("[store.cleanup] ephemeral_ttl_hours must be >= 1 (use capy_cleanup purge_ephemeral=true for one-shot aggressive purging)")
	}
	if cfg.Store.Cleanup.SessionTTLDays < 1 {
		return fmt.Errorf("[store.cleanup] session_ttl_days must be >= 1 (use capy_cleanup purge_session=true for one-shot aggressive purging)")
	}
	if cfg.Search.WindowMS < 1000 || cfg.Search.WindowMS > 3600000 {
		return fmt.Errorf("[search] window_ms must be between 1000 and 3600000")
	}
	if cfg.Search.TaperAfter < 1 || cfg.Search.TaperAfter >= cfg.Search.BlockAfter || cfg.Search.BlockAfter > 10000 {
		return fmt.Errorf("[search] must satisfy 1 <= taper_after < block_after <= 10000")
	}
	return nil
}

// loadAndMerge reads a TOML file and merges non-zero values into cfg.
// Missing files are silently skipped.
func loadAndMerge(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var overlay Config
	if err := toml.Unmarshal(data, &overlay); err != nil {
		return err
	}

	// Validate explicit values that the zero-aware merger can't distinguish
	// from "unset". Pointer-based detection catches user-written 0s.
	var detect detectionOverlay
	if err := toml.Unmarshal(data, &detect); err != nil {
		return err
	}
	if detect.Executor.Timeout != nil {
		value := *detect.Executor.Timeout
		if value < 1 || int64(value) > math.MaxInt64/int64(time.Second) {
			return fmt.Errorf("[executor] timeout must be positive integral seconds within duration range")
		}
		cfg.Executor.Timeout = value
	}
	if err := mergeRuns(&cfg.Runs, detect.Runs); err != nil {
		return err
	}
	if detect.Store.Cleanup.EphemeralTTLHours != nil && *detect.Store.Cleanup.EphemeralTTLHours < 1 {
		return fmt.Errorf("[store.cleanup] ephemeral_ttl_hours must be >= 1 (use capy_cleanup purge_ephemeral=true for one-shot aggressive purging)")
	}
	if detect.Store.Cleanup.SessionTTLDays != nil && *detect.Store.Cleanup.SessionTTLDays < 1 {
		return fmt.Errorf("[store.cleanup] session_ttl_days must be >= 1 (use capy_cleanup purge_session=true for one-shot aggressive purging)")
	}
	// Validate each supplied scalar even if a later file would override it.
	// The relationship between thresholds is checked only after all layers merge.
	for _, field := range []struct {
		name      string
		value     *int
		target    *int
		low, high int
	}{
		{"window_ms", detect.Search.WindowMS, &cfg.Search.WindowMS, 1000, 3600000},
		{"taper_after", detect.Search.TaperAfter, &cfg.Search.TaperAfter, 1, 9999},
		{"block_after", detect.Search.BlockAfter, &cfg.Search.BlockAfter, 2, 10000},
	} {
		if field.value == nil {
			continue
		}
		if *field.value < field.low || *field.value > field.high {
			return fmt.Errorf("[search] %s must be between %d and %d", field.name, field.low, field.high)
		}
		*field.target = *field.value
	}

	mergeConfig(cfg, &overlay)
	// An explicit empty path clears a lower-priority credential file.
	if detect.Store.KeyFile != nil {
		cfg.Store.KeyFile = *detect.Store.KeyFile
	}
	// An explicit zero disables a lower-priority size filter; omission inherits it.
	if detect.Vault.MinSessionBytes != nil {
		if *detect.Vault.MinSessionBytes < 0 {
			return fmt.Errorf("[vault] min_session_bytes must be >= 0 (0 disables size filtering)")
		}
		cfg.Vault.MinSessionBytes = *detect.Vault.MinSessionBytes
	}
	return nil
}

// detectionOverlay uses pointer fields to distinguish "user wrote a zero value" from
// "user omitted the key", which the zero-value merge in mergeConfig can't do.
// Keys needing zero-rejection or an explicit zero override appear here.
type detectionOverlay struct {
	Executor struct {
		Timeout *int `toml:"timeout"`
	} `toml:"executor"`
	Runs   runsOverlay `toml:"runs"`
	Search struct {
		WindowMS   *int `toml:"window_ms"`
		TaperAfter *int `toml:"taper_after"`
		BlockAfter *int `toml:"block_after"`
	} `toml:"search"`
	Vault struct {
		MinSessionBytes *int64 `toml:"min_session_bytes"`
	} `toml:"vault"`
	Store struct {
		KeyFile *string `toml:"key_file"`
		Cleanup struct {
			EphemeralTTLHours *int `toml:"ephemeral_ttl_hours"`
			SessionTTLDays    *int `toml:"session_ttl_days"`
		} `toml:"cleanup"`
	} `toml:"store"`
}

// mergeConfig overwrites fields in dst with non-zero values from src.
func mergeConfig(dst, src *Config) {
	// Store
	if src.Store.Path != "" {
		dst.Store.Path = src.Store.Path
	}
	if src.Store.TitleWeight != 0 {
		dst.Store.TitleWeight = src.Store.TitleWeight
	}
	if src.Store.MaxSourceBytes != 0 {
		dst.Store.MaxSourceBytes = src.Store.MaxSourceBytes
	}
	if src.Store.Cleanup.ColdThresholdDays != 0 {
		dst.Store.Cleanup.ColdThresholdDays = src.Store.Cleanup.ColdThresholdDays
	}
	if src.Store.Cleanup.EphemeralTTLHours != 0 {
		dst.Store.Cleanup.EphemeralTTLHours = src.Store.Cleanup.EphemeralTTLHours
	}
	if src.Store.Cleanup.SessionTTLDays != 0 {
		dst.Store.Cleanup.SessionTTLDays = src.Store.Cleanup.SessionTTLDays
	}
	if src.Store.Cleanup.AutoPrune {
		dst.Store.Cleanup.AutoPrune = true
	}
	if src.Store.Cache.FetchTTLHours != 0 {
		dst.Store.Cache.FetchTTLHours = src.Store.Cache.FetchTTLHours
	}

	// Executor
	if src.Executor.Timeout != 0 {
		dst.Executor.Timeout = src.Executor.Timeout
	}
	if src.Executor.MaxOutputBytes != 0 {
		dst.Executor.MaxOutputBytes = src.Executor.MaxOutputBytes
	}

	// Server
	if src.Server.LogLevel != "" {
		dst.Server.LogLevel = src.Server.LogLevel
	}
}

// xdgConfigPath returns $XDG_CONFIG_HOME or ~/.config.
func xdgConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}
