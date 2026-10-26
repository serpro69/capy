package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/platform"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestDoctorRunStoreParity(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, variant := range []string{"missing", "ready", "wrong key", "policy mismatch"} {
		t.Run(variant, func(t *testing.T) {
			project := stdioProject(t)
			key := "synthetic-run-diagnostic-key"
			env := stdioEnv(t, key)
			path, err := config.ResolveRunDBPath(project)
			require.NoError(t, err)
			if variant != "missing" {
				s, err := runstore.Open(t.Context(), path, key, true)
				require.NoError(t, err)
				require.NoError(t, s.Close())
			}
			switch variant {
			case "wrong key":
				key = "synthetic-wrong-run-diagnostic-key"
				env = stdioEnv(t, key)
			case "policy mismatch":
				require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[runs]\nttl_hours=48\n"), 0600))
			}
			cmd := exec.Command(bin, "doctor", "--project-dir", project)
			cmd.Dir, cmd.Env = project, env
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: env})
			p.initialize(t)
			mcp := p.tool(t, "capy_doctor", map[string]any{})
			p.close(t)
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "] Run ") {
					require.Contains(t, mcp, line, "CLI and MCP must use the same run-store checks")
				}
			}
			require.NotContains(t, string(out), key)
			require.NotContains(t, mcp, key)
			if variant == "missing" {
				require.NoDirExists(t, filepath.Dir(path))
			}
			if variant == "policy mismatch" {
				report, err := runstore.Inspect(t.Context(), path, key)
				require.NoError(t, err)
				require.Equal(t, 24, report.Policy.TTLHours, "doctor must not publish requested policy")
				require.EqualValues(t, 1, report.Policy.Revision)
			}
		})
	}
}

func TestDoctorRunOwnerUsesCaptureSelection(t *testing.T) {
	bin := buildStdioCapy(t)
	project := stdioProject(t)
	_, env := installCodexLaunch(t, bin, project)
	subdir := filepath.Join(project, "sub")
	require.NoError(t, os.Mkdir(subdir, 0700))
	cmd := exec.Command(bin, "doctor")
	cmd.Dir, cmd.Env = subdir, env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	want := platform.CheckRuns(t.Context(), project, "", config.DefaultConfig().Runs)
	require.Contains(t, string(out), want[0].String())
	require.NoDirExists(t, filepath.Join(project, ".capy", "runs"))
	require.NoDirExists(t, filepath.Join(subdir, ".capy", "runs"))
}

func TestDoctorRunInvalidConfigReportsCapability(t *testing.T) {
	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[invalid"), 0600))
	cmd := newDoctorCmd()
	cmd.Flags().String("project-dir", project, "")
	checks := runChecks(cmd)
	require.Len(t, checks, 2)
	require.Equal(t, platform.CheckRunCapability(), checks[0])
	require.Equal(t, "Run store", checks[1].Name)
	require.Equal(t, platform.Fail, checks[1].Status)
	require.Contains(t, checks[1].Detail, "no fallback store was inspected")
	require.NoDirExists(t, filepath.Join(project, ".capy", "runs"))
}

func TestCapturedPreviewLimitCLI(t *testing.T) {
	bin := buildStdioCapy(t)
	project := cliProject(t, "[executor]\nmax_output_bytes=12\n")
	env := stdioEnv(t, "synthetic-cli-preview-key")
	for _, extra := range [][]string{nil, {"--intent", "marker"}} {
		run := cliRun(t, bin, project, env, "printf 'ééé marker and retained tail'", extra...)
		require.NotNil(t, run.RunID)
		require.Equal(t, 0, *run.ExitCode)
		require.True(t, run.MetadataOmitted)
		require.LessOrEqual(t, len(run.Preview), 12)
		require.True(t, utf8.ValidString(run.Preview))
		out, diag, status := capturedCLI(t, bin, project, env, "runs", "read", "--run-id", *run.RunID, "--raw", "--stream", "stdout", "--json")
		require.Zero(t, status)
		require.Empty(t, diag)
		var page runstore.ReadPage
		require.NoError(t, json.Unmarshal(out, &page))
		var raw []byte
		for _, fragment := range page.Fragments {
			raw = append(raw, fragment.Data...)
		}
		require.Equal(t, "ééé marker and retained tail", string(raw))
		require.EqualValues(t, len(raw), run.Capture.CapturedBytes)
	}
}
