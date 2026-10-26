//go:build darwin && cgo && integration

package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

const darwinCaptureKey = "synthetic-darwin-capture-key"

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == SupervisorCommand {
		if os.Getenv("CAPY_DARWIN_CAPTURE_MODE") == "hold close" {
			// This fixture deliberately SIGSTOPs the helper around authorization.
			// Darwin sends HUP+CONT to newly orphaned stopped groups on owner
			// exit. Ignore fixture-induced HUP to exercise control EOF cleanup;
			// production signal handling is unchanged. CONT may resume it early.
			signal.Ignore(syscall.SIGHUP)
		}
		if err := ServeInherited(context.Background(), openDarwinCaptureFixture); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "darwin-capture-owner" {
		if err := runDarwinCaptureOwner(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "darwin-escaped-leader" {
		child := exec.Command(os.Args[0], "darwin-escaped-member")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			panic(err)
		}
		root := os.Getenv("CAPY_DARWIN_CAPTURE_ROOT")
		if err := os.WriteFile(filepath.Join(root, "escaped.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			child.Process.Kill()
			child.Wait()
			panic(err)
		}
		os.Exit(9)
	}
	if len(os.Args) == 2 && os.Args[1] == "darwin-escaped-member" {
		if err := awaitLeaseFile(context.Background(), filepath.Join(os.Getenv("CAPY_DARWIN_CAPTURE_ROOT"), "release-member")); err != nil {
			os.Exit(91)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The fixture uses the real authenticated store and only injects failures at
// its persistence seam. No test mode or public capability override is shipped.
type darwinCaptureFixture struct {
	CaptureStore
	root, mode string
	appends    int
}

func openDarwinCaptureFixture(ctx context.Context, start Startup, key string, leases Leases) (*Run, error) {
	run, err := OpenStoredRun(ctx, start, key, leases)
	if err != nil {
		return nil, err
	}
	mode := os.Getenv("CAPY_DARWIN_CAPTURE_MODE")
	root := os.Getenv("CAPY_DARWIN_CAPTURE_ROOT")
	if mode != "" {
		run.Store = &darwinCaptureFixture{CaptureStore: run.Store, root: root, mode: mode}
	}
	return run, nil
}

func (s *darwinCaptureFixture) Append(event executor.OutputEvent) error {
	if s.mode == "append failure" && s.appends > 0 {
		return errors.New("synthetic append failure")
	}
	if err := s.CaptureStore.Append(event); err != nil {
		return err
	}
	s.appends++
	if s.mode == "append failure" {
		return os.WriteFile(filepath.Join(s.root, "prefix-committed"), nil, 0600)
	}
	return nil
}

func (s *darwinCaptureFixture) Finish(ctx context.Context, out Outcome) error {
	if s.mode == "finish failure" {
		return errors.New("synthetic finalization failure")
	}
	if err := s.CaptureStore.Finish(ctx, out); err != nil {
		return err
	}
	if s.mode == "lost notice" {
		// Commit and close, then lose the terminal notice. Recovery must use the
		// row's proof without repeating the command.
		if err := s.CaptureStore.Close(); err != nil {
			return err
		}
		os.Exit(73)
	}
	return nil
}

func (s *darwinCaptureFixture) Close() error {
	if s.mode == "hold close" {
		if err := os.WriteFile(filepath.Join(s.root, "closing"), nil, 0600); err != nil {
			return errors.Join(err, s.CaptureStore.Close())
		}
		if err := awaitLeaseFile(context.Background(), filepath.Join(s.root, "release-close")); err != nil {
			return errors.Join(err, s.CaptureStore.Close())
		}
	}
	return s.CaptureStore.Close()
}

func newDarwinCaptureService(t *testing.T, mode string) *Service {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CAPY_DARWIN_CAPTURE_ROOT", root)
	t.Setenv("CAPY_DARWIN_CAPTURE_MODE", mode)
	s := NewService(root, darwinCaptureKey, config.DefaultConfig().Runs)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

// Bypass only public capability/admission in this private fixture. The real
// service, acquired references, launcher, helper and recovery paths run intact.
func executePrivateDarwin(t *testing.T, s *Service, req executor.ExecRequest) runstore.Result {
	t.Helper()
	ctx, done, err := s.begin(t.Context(), true)
	require.NoError(t, err)
	defer done()
	st, owner, err := s.attached(ctx, true)
	require.NoError(t, err)
	row, err := st.Admit(ctx, owner, executor.CleanupDarwinObservation, runstore.Program{
		Owner: s.project, Request: req, Deadline: time.Now().Add(30 * time.Second),
	})
	require.NoError(t, err)
	call := &runCall{ready: make(chan runstore.Result, 1)}
	result := s.executeAdmitted(ctx, st, owner, row, call)
	require.Nil(t, call.cleanup, "fixture must join its helper before releasing the service reservation")
	return result
}

func darwinRawText(t *testing.T, s *Service, id, stream string) string {
	t.Helper()
	page, err := s.ReadRaw(t.Context(), id, stream, "", 65536)
	require.NoError(t, err)
	require.True(t, page.EndOfAvailable)
	var b strings.Builder
	for _, fragment := range page.Fragments {
		b.Write(fragment.Data)
	}
	return b.String()
}

func TestDarwinCapturedRecovery(t *testing.T) {
	for _, mode := range []string{"ordinary", "nonzero", "lost notice"} {
		t.Run(mode, func(t *testing.T) {
			fixtureMode, exit := "", 0
			if mode == "nonzero" {
				exit = 7
			}
			if mode == "lost notice" {
				fixtureMode = mode
			}
			s := newDarwinCaptureService(t, fixtureMode)
			secret := "sk-" + strings.Repeat("a", 48)
			stdout := "stdout " + secret + "\n"
			result := executePrivateDarwin(t, s, executor.ExecRequest{Language: executor.Shell,
				Code: fmt.Sprintf("printf x >> once; printf 'stdout %s\\n'; printf 'stderr\\n' >&2; exit %d", secret, exit)})
			require.Equal(t, "exited", result.State)
			require.Equal(t, &exit, result.ExitCode)
			require.Equal(t, executor.CleanupEvidence{Scope: executor.CleanupDarwinObservation, State: executor.CleanupObserved}, result.Cleanup)
			require.True(t, result.Capture.TerminalPersisted)
			require.Equal(t, "complete", result.Capture.State)
			require.Equal(t, stdout, darwinRawText(t, s, *result.RunID, "stdout"))
			require.Equal(t, "stderr\n", darwinRawText(t, s, *result.RunID, "stderr"))
			page, err := s.ReadProjection(t.Context(), *result.RunID, "stdout", "", 65536)
			require.NoError(t, err)
			var projection strings.Builder
			for _, fragment := range page.Fragments {
				projection.WriteString(fragment.Text)
			}
			require.Contains(t, projection.String(), "stdout")
			require.NotContains(t, projection.String(), secret)
			require.NotContains(t, fmt.Sprint(result.Preview), secret)
			count, err := os.ReadFile(filepath.Join(s.project, "once"))
			require.NoError(t, err)
			require.Equal(t, "x", string(count))
			path := s.store.Path()
			require.NoError(t, s.Close())
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NotContains(t, string(data), secret)
			require.NotContains(t, string(data), "SQLite format 3")
			wrong, err := runstore.Open(t.Context(), path, "synthetic-wrong-key", false)
			require.Error(t, err)
			require.Nil(t, wrong)
			reopened := NewService(s.project, darwinCaptureKey, config.DefaultConfig().Runs)
			defer reopened.Close()
			recovered, err := reopened.Status(t.Context(), *result.RunID)
			require.NoError(t, err)
			require.Equal(t, result.Cleanup, recovered.Cleanup)
			require.Equal(t, result.ExitCode, recovered.ExitCode)
			require.Equal(t, stdout, darwinRawText(t, reopened, *result.RunID, "stdout"))
		})
	}
}

func TestDarwinCapturedPersistenceFailures(t *testing.T) {
	for _, mode := range []string{"append failure", "finish failure"} {
		t.Run(mode, func(t *testing.T) {
			s := newDarwinCaptureService(t, mode)
			code := "printf prefix; "
			if mode == "append failure" {
				code += "while [ ! -f prefix-committed ]; do sleep 0.01; done; printf tail; "
			}
			code += "printf x >> once; exit 7"
			result := executePrivateDarwin(t, s, executor.ExecRequest{Language: executor.Shell, Code: code})
			exit := 7
			require.Equal(t, &exit, result.ExitCode, "storage failure must preserve actual command status")
			require.Equal(t, "incomplete", result.Capture.State)
			count, err := os.ReadFile(filepath.Join(s.project, "once"))
			require.NoError(t, err)
			require.Equal(t, "x", string(count), "capture failure must drain without stopping/retrying the command")
			require.Equal(t, "prefix", darwinRawText(t, s, *result.RunID, "stdout"))
			if mode == "append failure" {
				require.Equal(t, executor.CleanupObserved, result.Cleanup.State)
				require.True(t, result.Capture.TerminalPersisted)
				next := executePrivateDarwin(t, s, executor.ExecRequest{Language: executor.Shell, Code: "true"})
				require.Equal(t, executor.CleanupObserved, next.Cleanup.State)
			} else {
				require.Equal(t, executor.CleanupPending, result.Cleanup.State)
				require.False(t, result.Capture.TerminalPersisted)
				_, done, err := s.begin(t.Context(), true)
				if done != nil {
					done()
				}
				require.ErrorContains(t, err, "unverified run cleanup", "the live service must refuse new work")
				_, err = s.store.Admit(t.Context(), s.owner, executor.CleanupDarwinObservation,
					runstore.Program{Owner: s.project, Request: executor.ExecRequest{Language: executor.Shell, Code: "touch forbidden"}})
				require.ErrorContains(t, err, "unverified cleanup", "the live owner's durable admission must also be blocked")
				path := s.store.Path()
				require.NoError(t, s.Close())
				st, err := runstore.Open(t.Context(), path, darwinCaptureKey, false)
				require.NoError(t, err)
				defer st.Close()
				row, err := st.Load(t.Context(), *result.RunID)
				require.NoError(t, err)
				require.Equal(t, executor.CleanupPending, row.Cleanup.State)
				_, err = st.ClaimOwner(t.Context())
				require.ErrorContains(t, err, "unverified cleanup")
			}
		})
	}
}

func TestDarwinCapturedCompilerRuntimeFailure(t *testing.T) {
	s := newDarwinCaptureService(t, "")
	require.NoError(t, os.WriteFile(filepath.Join(s.project, "rustc"), []byte("#!/bin/sh\nprintf compiler-output\nexit 0\n"), 0700))
	t.Setenv("PATH", s.project+string(os.PathListSeparator)+os.Getenv("PATH"))
	result := executePrivateDarwin(t, s, executor.ExecRequest{Language: executor.Rust, Code: "fixture"})
	require.Equal(t, "start_failed", result.State)
	require.Equal(t, "runtime_unavailable", result.Error.Code)
	require.Equal(t, executor.CleanupObserved, result.Cleanup.State)
	require.True(t, result.Capture.TerminalPersisted)
	require.Equal(t, "compiler-output", darwinRawText(t, s, *result.RunID, "stdout"))
}

func TestDarwinCapturedPublicAdmissionDisabled(t *testing.T) {
	s := newDarwinCaptureService(t, "")
	require.Error(t, Supported())
	result := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "touch forbidden"}, "", time.Second)
	require.Nil(t, result.RunID)
	require.Equal(t, "storage_unavailable", result.Error.Code)
	require.NoFileExists(t, filepath.Join(s.project, "forbidden"))
	require.NoDirExists(t, filepath.Join(s.project, ".capy", "runs"))
}

func TestDarwinCapturedObservationFailure(t *testing.T) {
	s := newDarwinCaptureService(t, "")
	exe, err := os.Executable()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.WriteFile(filepath.Join(s.project, "release-member"), nil, 0600))
		data, err := os.ReadFile(filepath.Join(s.project, "escaped.pid"))
		if os.IsNotExist(err) {
			return
		}
		require.NoError(t, err)
		pid, err := strconv.Atoi(string(data))
		require.NoError(t, err)
		require.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 5*time.Second, 5*time.Millisecond)
	})
	code := "exec '" + strings.ReplaceAll(exe, "'", "'\"'\"'") + "' darwin-escaped-leader"
	result := executePrivateDarwin(t, s, executor.ExecRequest{Language: executor.Shell, Code: code})
	exit := 9
	require.Equal(t, &exit, result.ExitCode)
	require.Equal(t, "interrupted", result.State)
	require.Equal(t, executor.CleanupEvidence{Scope: executor.CleanupDarwinObservation, State: executor.CleanupPending}, result.Cleanup)
	require.Equal(t, "incomplete", result.Capture.State)
	require.True(t, result.Capture.TerminalPersisted, "a persisted outcome does not imply completed cleanup")
	require.Equal(t, "cleanup_unverified", result.Error.Code)
	_, done, err := s.begin(t.Context(), true)
	if done != nil {
		done()
	}
	require.ErrorContains(t, err, "unverified run cleanup", "the service must quarantine pending Darwin proof")
}
