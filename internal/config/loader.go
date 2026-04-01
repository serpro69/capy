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

	return cfg, nil
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

	mergeConfig(cfg, &overlay)
	return nil
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
	if src.Store.Cleanup.AutoPrune {
		dst.Store.Cleanup.AutoPrune = true
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
