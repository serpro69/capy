package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchConfig(t *testing.T) {
	for _, tt := range []struct {
		name, global, project, root string
		want                        SearchConfig
		wantError                   string
	}{
		{name: "defaults", want: SearchConfig{60000, 3, 8}},
		{name: "empty table inherits", global: "window_ms = 2000\ntaper_after = 5\nblock_after = 9", want: SearchConfig{2000, 5, 9}},
		{name: "three layers", global: "window_ms = 2000\ntaper_after = 5\nblock_after = 9", project: "window_ms = 3000\nblock_after = 12", root: "window_ms = 4000", want: SearchConfig{4000, 5, 12}},
		{name: "relationship validated after merge", global: "taper_after = 20", project: "block_after = 30", want: SearchConfig{60000, 20, 30}},
		{name: "minimum", root: "window_ms = 1000\ntaper_after = 1\nblock_after = 2", want: SearchConfig{1000, 1, 2}},
		{name: "maximum", root: "window_ms = 3600000\ntaper_after = 9999\nblock_after = 10000", want: SearchConfig{3600000, 9999, 10000}},
		{name: "equal thresholds", root: "taper_after = 8", wantError: "taper_after < block_after"},
		{name: "inverted merged thresholds", global: "taper_after = 6\nblock_after = 10", root: "block_after = 5", wantError: "taper_after < block_after"},
		{name: "invalid lower layer cannot be hidden", global: "window_ms = 0", root: "window_ms = 2000", wantError: "window_ms"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			project, xdg := t.TempDir(), t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)
			for _, layer := range []struct{ path, values string }{
				{filepath.Join(xdg, "capy", "config.toml"), tt.global},
				{filepath.Join(project, ".capy", "config.toml"), tt.project},
				{filepath.Join(project, ".capy.toml"), tt.root},
			} {
				require.NoError(t, os.MkdirAll(filepath.Dir(layer.path), 0o700))
				require.NoError(t, os.WriteFile(layer.path, []byte("[search]\n"+layer.values), 0o600))
			}
			cfg, err := Load(project)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Search)
		})
	}
}

func TestSearchConfigRejectsInvalidScalars(t *testing.T) {
	for _, field := range []struct {
		name       string
		outOfRange []string
	}{
		{"window_ms", []string{"999", "3600001"}},
		{"taper_after", []string{"10000"}},
		{"block_after", []string{"1", "10001"}},
	} {
		values := append([]string{"0", "-1", "1.5", "3.0", `"3"`, "true", "[]", "{}", "nan", "inf", "9223372036854775808"}, field.outOfRange...)
		for _, value := range values {
			t.Run(fmt.Sprintf("%s=%s", field.name, value), func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				project := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[search]\n"+field.name+" = "+value), 0o600))
				_, err := Load(project)
				require.Error(t, err)
			})
		}
	}
}
