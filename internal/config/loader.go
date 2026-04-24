package config

import (
	"fmt"
	"os"
	"path/filepath"

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
	if cfg.Store.Cleanup.EphemeralTTLHours < 1 {
		return fmt.Errorf("[store.cleanup] ephemeral_ttl_hours must be >= 1 (use capy_cleanup purge_ephemeral=true for one-shot aggressive purging)")
	}
	if cfg.Store.Cleanup.SessionTTLDays < 1 {
		return fmt.Errorf("[store.cleanup] session_ttl_days must be >= 1 (use capy_cleanup purge_session=true for one-shot aggressive purging)")
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
	if detect.Store.Cleanup.EphemeralTTLHours != nil && *detect.Store.Cleanup.EphemeralTTLHours < 1 {
		return fmt.Errorf("[store.cleanup] ephemeral_ttl_hours must be >= 1 (use capy_cleanup purge_ephemeral=true for one-shot aggressive purging)")
	}
	if detect.Store.Cleanup.SessionTTLDays != nil && *detect.Store.Cleanup.SessionTTLDays < 1 {
		return fmt.Errorf("[store.cleanup] session_ttl_days must be >= 1 (use capy_cleanup purge_session=true for one-shot aggressive purging)")
	}

	mergeConfig(cfg, &overlay)
	return nil
}

// detectionOverlay uses pointer fields to distinguish "user wrote 0" from
// "user omitted the key", which the zero-value merge in mergeConfig can't do.
// Only the keys that need strict zero-rejection appear here.
type detectionOverlay struct {
	Store struct {
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
