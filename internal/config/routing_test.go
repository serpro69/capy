package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutineRoutingConfig(t *testing.T) {
	for _, tc := range []struct {
		name, global, project, local string
		enabled                      bool
		allow                        []string
		invalid                      bool
	}{
		{name: "defaults", enabled: true},
		{name: "false survives omission", global: "enabled=false\nnative_allow=['Bash(make:*)']", project: "", enabled: false, allow: []string{"Bash(make:*)"}},
		{name: "highest clears exceptions", global: "enabled=false\nnative_allow=['Bash(make:*)']", project: "enabled=true", local: "native_allow=[]", enabled: true, allow: []string{}},
		{name: "highest disables", global: "enabled=true", project: "native_allow=['Bash(go test:*)']", local: "enabled=false", enabled: false, allow: []string{"Bash(go test:*)"}},
		{name: "not a bool", project: "enabled='false'", invalid: true},
		{name: "wrong tool", project: "native_allow=['Read(*)']", invalid: true},
		{name: "empty glob", project: "native_allow=['Bash()']", invalid: true},
		{name: "blank glob", project: "native_allow=['Bash( )']", invalid: true},
		{name: "raw command", project: "native_allow=['make test']", invalid: true},
		{name: "invalid lower layer", global: "native_allow=['Read(*)']", local: "native_allow=[]", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, project := t.TempDir(), t.TempDir()
			for _, env := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
				t.Setenv(env, home)
			}
			for _, file := range []struct{ path, body string }{
				{filepath.Join(home, "capy", "config.toml"), tc.global},
				{filepath.Join(project, ".capy", "config.toml"), tc.project},
				{filepath.Join(project, ".capy.toml"), tc.local},
			} {
				require.NoError(t, os.MkdirAll(filepath.Dir(file.path), 0700))
				require.NoError(t, os.WriteFile(file.path, []byte("[routing.routine]\n"+file.body), 0600))
			}
			cfg, err := Load(project)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.enabled, cfg.Routing.Routine.Enabled)
			require.Equal(t, tc.allow, cfg.Routing.Routine.NativeAllow)
		})
	}
}
