package config

// Config is the top-level configuration for capy.
type Config struct {
	Store    StoreConfig    `toml:"store"`
	Executor ExecutorConfig `toml:"executor"`
	Server   ServerConfig   `toml:"server"`
}

// StoreConfig controls the FTS5 knowledge base.
type StoreConfig struct {
	Path    string        `toml:"path"`
	Cleanup CleanupConfig `toml:"cleanup"`
}

// CleanupConfig controls cold-source pruning.
type CleanupConfig struct {
	ColdThresholdDays int  `toml:"cold_threshold_days"`
	AutoPrune         bool `toml:"auto_prune"`
}

// ExecutorConfig controls the polyglot executor.
type ExecutorConfig struct {
	Timeout        int `toml:"timeout"`          // seconds
	MaxOutputBytes int `toml:"max_output_bytes"` // hard cap on captured output
}

// ServerConfig controls the MCP server.
type ServerConfig struct {
	LogLevel string `toml:"log_level"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Store: StoreConfig{
			Cleanup: CleanupConfig{
				ColdThresholdDays: 30,
				AutoPrune:         false,
			},
		},
		Executor: ExecutorConfig{
			Timeout:        30,
			MaxOutputBytes: 102400,
		},
		Server: ServerConfig{
			LogLevel: "info",
		},
	}
}
