package config

import (
	"testing"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Frozen path-bearing subset of v0.16.4's Config/StoreConfig and its default
// toml.Unmarshal call. Deliberately do not reuse today's StoreConfig: adding
// KeyFile there must not make this rollback compatibility check pass by accident.
// This tests decoding only, not a complete downgrade or legacy MCP launch.
func TestLegacyV0164ConfigIgnoresKeyFile(t *testing.T) {
	for _, path := range []string{".capy/knowledge.db", "../shared/knowledge.db", "/tmp/shared/knowledge.db"} {
		t.Run(path, func(t *testing.T) {
			var legacy struct {
				Store struct {
					Path string `toml:"path"`
				} `toml:"store"`
			}
			data := []byte("[store]\npath = '" + path + "'\nkey_file = 'missing-on-purpose.key'\n")
			require.NoError(t, toml.Unmarshal(data, &legacy))
			assert.Equal(t, path, legacy.Store.Path)
		})
	}
}
