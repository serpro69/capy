//go:build linux && integration

package execution

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"golang.org/x/sys/unix"
)

const (
	fixtureRun        = "11111111-1111-4111-8111-111111111111"
	fixtureGeneration = "22222222-2222-4222-8222-222222222222"
	fixtureKey        = "private-descriptor-only-synthetic-credential"
)

type fixtureConfig struct {
	Code                                           string
	Language                                       executor.Language
	Generation                                     string
	PendingFail, AppendFail, FinishFail, HoldClose bool
	HoldOpen                                       bool
	HoldStarted                                    bool
	AppendDelay                                    time.Duration
}

// These files contain synthetic fixture data only. Production uses the injected
// encrypted runstore, not this backend or a plaintext output/proof spool.
type fixtureStore struct {
	dir    string
	cfg    fixtureConfig
	events *os.File
}

func (s *fixtureStore) Pending(ctx context.Context) error {
	if s.cfg.PendingFail {
		return errors.New("synthetic pending failure")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeProof(s.dir, "pending")
}
func (s *fixtureStore) Append(event executor.OutputEvent) error {
	if s.cfg.AppendDelay > 0 {
		time.Sleep(s.cfg.AppendDelay)
	}
	if s.cfg.AppendFail {
		return errors.New("synthetic append failure")
	}
	return json.NewEncoder(s.events).Encode(event)
}
func (s *fixtureStore) Finish(ctx context.Context, outcome Outcome) error {
	if s.cfg.FinishFail {
		return errors.New("synthetic finalization failure")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !outcome.Result.Retired {
		return errors.New("cleanup unverified")
	}
	return writeProof(s.dir, "retired")
}
func (s *fixtureStore) Close() error {
	if s.cfg.HoldClose {
		if err := os.WriteFile(filepath.Join(s.dir, "closing"), nil, 0o600); err != nil {
			return err
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(s.dir, "release")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("fixture close gate timed out")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return s.events.Close()
}

func writeProof(dir, state string) error {
	f, err := os.OpenFile(filepath.Join(dir, "proof"), os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(state); err != nil {
		return err
	}
	return f.Sync()
}

func openFixture(ctx context.Context, start Startup, key string, _ Leases) (*Run, error) {
	if key != fixtureKey || start.RunID != fixtureRun {
		return nil, errors.New("fixture admission mismatch")
	}
	b, err := os.ReadFile(filepath.Join(start.StorePath, "fixture.json"))
	if err != nil {
		return nil, err
	}
	var cfg fixtureConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if cfg.Generation != start.Generation {
		return nil, errors.New("fixture generation mismatch")
	}
	if cfg.HoldOpen {
		if err := os.WriteFile(filepath.Join(start.StorePath, "opening.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			return nil, err
		}
		for {
			if _, err := os.Stat(filepath.Join(start.StorePath, "release-open")); err == nil {
				break
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	proof, err := os.ReadFile(filepath.Join(start.StorePath, "proof"))
	if err != nil || string(proof) != "not_spawned" {
		return nil, errors.New("fixture cleanup unverified")
	}
	events, err := os.OpenFile(filepath.Join(start.StorePath, "events"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	run := &Run{
		Executor: executor.NewExecutor(start.StorePath, 0),
		Request:  executor.ExecRequest{Language: cfg.Language, Code: cfg.Code},
		Store:    &fixtureStore{dir: start.StorePath, cfg: cfg, events: events},
	}
	if cfg.HoldStarted {
		run.Started = func(string) error {
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(start.StorePath, "release-started")); err == nil {
					return nil
				}
				time.Sleep(time.Millisecond)
			}
			return errors.New("fixture started gate timed out")
		}
	}
	return run, nil
}

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == SupervisorCommand {
		open := openFixture
		if os.Getenv("CAPY_STORED_FIXTURE") != "" {
			open = openStoredFixture
		}
		if err := ServeInherited(context.Background(), open); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "fixture-owner" {
		os.Exit(runFixtureOwner())
	}
	if len(os.Args) == 2 && os.Args[1] == "stored-fixture-owner" {
		os.Exit(runStoredFixtureOwner())
	}
	// Adopt intentionally orphaned helpers in crash fixtures so the tests, even
	// in containers with an unreaping PID 1, leave no zombie processes behind.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func runFixtureOwner() int {
	dir := os.Getenv("CAPY_SUPERVISOR_FIXTURE")
	leases, err := lockFixture(dir)
	if err != nil {
		return 1
	}
	defer leases.Owner.Close()
	defer leases.Lifecycle.Close()
	exe, err := os.Executable()
	if err != nil {
		return 1
	}
	h, err := launch(context.Background(), exe, fixtureStartup(dir), fixtureKey, leases)
	if err != nil {
		if h != nil {
			h.Wait()
		}
		return 1
	}
	if err := os.WriteFile(filepath.Join(dir, "helper.pid"), []byte(strconv.Itoa(h.cmd.Process.Pid)), 0o600); err != nil {
		h.Cancel()
		h.Wait()
		return 1
	}
	_, err = h.Wait()
	if err != nil {
		return 1
	}
	return 0
}

func TestStoppedHelperDoesNotBlockAdmission(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			dir := fixture(t, fixtureConfig{Code: "touch must-not-spawn", HoldOpen: true})
			leases, err := lockFixture(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer leases.Owner.Close()
			defer leases.Lifecycle.Close()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type launchResult struct {
				helper *Supervisor
				err    error
			}
			ready := make(chan launchResult, 1)
			go func() {
				h, err := launch(ctx, exe, fixtureStartup(dir), fixtureKey, leases)
				ready <- launchResult{h, err}
			}()
			pid, err := strconv.Atoi(string(waitFile(t, filepath.Join(dir, "opening.pid"))))
			if err != nil {
				t.Fatal(err)
			}
			if err := unix.Kill(pid, unix.SIGSTOP); err != nil {
				t.Fatal(err)
			}
			joined := false
			defer func() {
				if !joined {
					unix.Kill(pid, unix.SIGCONT)
				}
			}()
			if mode == "cancel" {
				cancel()
			}
			var got launchResult
			select {
			case got = <-ready:
			case <-time.After(4 * time.Second):
				t.Fatal("stopped helper blocked startup deadline")
			}
			if got.err == nil || got.helper == nil {
				t.Fatalf("failure lost helper ownership: %+v", got)
			}
			leases.Owner.Close()
			leases.Lifecycle.Close()
			for _, name := range []string{"owner.lock", "lifecycle.lock"} {
				probeLease(t, dir, name, true)
			}
			if err := os.WriteFile(filepath.Join(dir, "release-open"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := unix.Kill(pid, unix.SIGCONT); err != nil {
				t.Fatal(err)
			}
			if out, err := waitResult(t, got.helper); err == nil || out.Result.Retired {
				t.Fatalf("failed startup certified cleanup: %+v %v", out, err)
			}
			joined = true
			assertProof(t, dir, "not_spawned")
			if _, err := os.Stat(filepath.Join(dir, "must-not-spawn")); !os.IsNotExist(err) {
				t.Fatal("failed startup spawned")
			}
			for _, name := range []string{"owner.lock", "lifecycle.lock"} {
				probeLease(t, dir, name, false)
			}
		})
	}
}

func fixtureStartup(dir string) Startup {
	return Startup{Version: 1, RunID: fixtureRun, Generation: fixtureGeneration, StorePath: dir, Deadline: time.Now().Add(20 * time.Second)}
}

func fixture(t *testing.T, cfg fixtureConfig) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, dir)
	}
	t.Setenv("CAPY_DB_KEY", "synthetic-environment-key")
	t.Setenv("CAPY_VAULT_KEY", "synthetic-vault-key")
	if cfg.Language == "" {
		cfg.Language = executor.Shell
	}
	if cfg.Generation == "" {
		cfg.Generation = fixtureGeneration
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proof"), []byte("not_spawned"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func lockFixture(dir string) (Leases, error) {
	var leases Leases
	for i, name := range []string{"owner.lock", "lifecycle.lock"} {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			if leases.Owner != nil {
				leases.Owner.Close()
			}
			return Leases{}, err
		}
		mode := unix.LOCK_EX
		if i == 1 {
			mode = unix.LOCK_SH
		}
		if err := unix.Flock(int(f.Fd()), mode|unix.LOCK_NB); err != nil {
			f.Close()
			if leases.Owner != nil {
				leases.Owner.Close()
			}
			return Leases{}, err
		}
		if i == 0 {
			leases.Owner = f
		} else {
			leases.Lifecycle = f
		}
	}
	return leases, nil
}

func startFixture(t *testing.T, dir string, start Startup) (*Supervisor, Leases) {
	t.Helper()
	leases, err := lockFixture(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { leases.Owner.Close(); leases.Lifecycle.Close() })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h, err := launch(context.Background(), exe, start, fixtureKey, leases)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Cancel() })
	return h, leases
}

func waitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			return b
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", filepath.Base(path))
	return nil
}

func waitResult(t *testing.T, h *Supervisor) (Outcome, error) {
	t.Helper()
	type result struct {
		outcome Outcome
		err     error
	}
	ch := make(chan result, 1)
	go func() { outcome, err := h.Wait(); ch <- result{outcome, err} }()
	select {
	case r := <-ch:
		return r.outcome, r.err
	case <-time.After(12 * time.Second):
		h.Cancel()
		t.Fatal("supervisor did not retire")
		return Outcome{}, nil
	}
}

func readEvents(t *testing.T, dir string) []executor.OutputEvent {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 64*1024)
	var events []executor.OutputEvent
	for s.Scan() {
		var e executor.OutputEvent
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func assertProof(t *testing.T, dir, state string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "proof"))
	if err != nil || string(b) != state {
		t.Fatalf("proof = %q, %v; want %s", b, err, state)
	}
}

func TestSupervisorStreamsAndPrivateDescriptors(t *testing.T) {
	dir := fixture(t, fixtureConfig{Code: `for fd in 3 4 5 6 7; do if [ -e /proc/$$/fd/$fd ]; then exit 99; fi; done
printf early
while [ ! -e release-output ]; do sleep .01; done
printf late
printf diagnostic >&2
exit 7`})
	h, _ := startFixture(t, dir, fixtureStartup(dir))
	for _, name := range []string{"cmdline", "environ"} {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/%s", h.cmd.Process.Pid, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), fixtureKey) {
			t.Fatalf("private credential exposed through %s", name)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, _ := os.ReadFile(filepath.Join(dir, "events")); strings.Contains(string(b), "ZWFybHk=") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("early output was not delivered while running")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(dir, "release-output"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	outcome, err := waitResult(t, h)
	if err != nil || !outcome.Result.Retired || !outcome.TerminalPersisted || outcome.Result.ExitCode == nil || *outcome.Result.ExitCode != 7 {
		t.Fatalf("outcome: %+v, %v", outcome, err)
	}
	var stdout, stderr string
	for i, e := range readEvents(t, dir) {
		if e.Sequence != uint64(i+1) || len(e.Data) > executor.EventBlockBytes {
			t.Fatalf("bad event: %+v", e)
		}
		if e.Stream == "stdout" {
			stdout += string(e.Data)
		} else {
			stderr += string(e.Data)
		}
	}
	if stdout != "earlylate" || stderr != "diagnostic" {
		t.Fatalf("streams %q / %q", stdout, stderr)
	}
	assertProof(t, dir, "retired")
}

func TestSupervisorRejectsPrivateAdmission(t *testing.T) {
	for _, mode := range []string{"version", "generation", "credential", "oversized", "partial", "no_authorization", "unlocked_owner", "unlocked_lifecycle", "aliased_leases", "foreign_owner_lock", "foreign_lifecycle_lock", "helper_missing"} {
		t.Run(mode, func(t *testing.T) {
			dir := fixture(t, fixtureConfig{Code: "touch must-not-spawn"})
			leases, err := lockFixture(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer leases.Owner.Close()
			defer leases.Lifecycle.Close()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "helper_missing" {
				if _, err := launch(context.Background(), filepath.Join(dir, "missing"), fixtureStartup(dir), fixtureKey, leases); err == nil {
					t.Fatal("missing helper accepted")
				}
				assertProof(t, dir, "not_spawned")
				return
			}
			if mode == "unlocked_owner" {
				if err := unix.Flock(int(leases.Owner.Fd()), unix.LOCK_UN); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unlocked_lifecycle" {
				if err := unix.Flock(int(leases.Lifecycle.Fd()), unix.LOCK_UN); err != nil {
					t.Fatal(err)
				}
			}
			ownerFile, lifeFile := leases.Owner, leases.Lifecycle
			if mode == "aliased_leases" {
				lifeFile = ownerFile
			}
			if mode == "foreign_owner_lock" {
				ownerFile, err = os.OpenFile(filepath.Join(dir, "owner.lock"), os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer ownerFile.Close()
			}
			if mode == "foreign_lifecycle_lock" {
				lifeFile, err = os.OpenFile(filepath.Join(dir, "lifecycle.lock"), os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer lifeFile.Close()
			}
			var channels []*os.File
			for i := 0; i < 3; i++ {
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				channels = append(channels, r, w)
			}
			defer func() {
				for _, f := range channels {
					f.Close()
				}
			}()
			cmd := exec.Command(exe, SupervisorCommand)
			cmd.ExtraFiles = []*os.File{channels[0], channels[2], channels[5], ownerFile, lifeFile}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			channels[0].Close()
			channels[2].Close()
			channels[5].Close()
			start := fixtureStartup(dir)
			if mode == "version" {
				start.Version = 99
			}
			if mode == "generation" {
				start.Generation = "33333333-3333-4333-8333-333333333333"
			}
			key := fixtureKey
			if mode == "credential" {
				key = ""
			}
			switch mode {
			case "oversized":
				binary.Write(channels[1], binary.BigEndian, uint32(maxFrameBytes+1))
			case "partial":
				binary.Write(channels[1], binary.BigEndian, uint32(100))
				channels[1].Write([]byte("{"))
			default:
				writeFrame(channels[1], start)
			}
			writeFrame(channels[3], []byte(key))
			channels[3].Close()
			if mode == "no_authorization" {
				var ready notice
				if err := readFrame(channels[4], &ready); err != nil {
					t.Fatal(err)
				}
			}
			channels[1].Close()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("malformed admission accepted")
				}
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				<-done
				t.Fatal("startup did not terminate")
			}
			if _, err := os.Stat(filepath.Join(dir, "must-not-spawn")); !os.IsNotExist(err) {
				t.Fatal("invalid admission spawned a command")
			}
			assertProof(t, dir, "not_spawned")
			if mode == "foreign_lifecycle_lock" && verifyDescriptorLock(lifeFile, "READ") == nil {
				t.Fatal("validation acquired the missing shared lease")
			}
		})
	}
}

func TestSupervisorBrokenEventsPreservesExecution(t *testing.T) {
	dir := fixture(t, fixtureConfig{Code: `printf early; while [ ! -e release-output ]; do sleep .01; done; printf late; touch command-finished`})
	h, _ := startFixture(t, dir, fixtureStartup(dir))
	type waitOutcome struct {
		outcome Outcome
		err     error
	}
	waiting := make(chan waitOutcome, 1)
	go func() { out, err := h.Wait(); waiting <- waitOutcome{out, err} }()
	h.events.Close()
	select {
	case result := <-waiting:
		t.Fatalf("notice failure cancelled a waiting command: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(dir, "release-output"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(dir, "command-finished"))
	// Let the helper finish independently of broken owner-side notification.
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(dir, "proof"))
		if string(b) == "retired" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing durable retirement")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case result := <-waiting:
		if result.err == nil || result.outcome.TerminalPersisted {
			t.Fatalf("broken IPC certified terminal receipt: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("broken IPC stranded owner waiter")
	}
	assertProof(t, dir, "retired")
}

func TestSupervisorBoundsBinaryEvents(t *testing.T) {
	dir := fixture(t, fixtureConfig{Code: `head -c 1048576 /dev/zero; printf tail >&2`})
	h, _ := startFixture(t, dir, fixtureStartup(dir))
	outcome, err := waitResult(t, h)
	if err != nil || outcome.Result.CaptureLost || !outcome.Result.Retired {
		t.Fatalf("outcome: %+v %v", outcome, err)
	}
	count := 0
	for _, event := range readEvents(t, dir) {
		if len(event.Data) > executor.EventBlockBytes {
			t.Fatal("unbounded event")
		}
		if event.Stream == "stdout" {
			count += len(event.Data)
			for _, b := range event.Data {
				if b != 0 {
					t.Fatal("binary byte changed")
				}
			}
		}
	}
	if count != 1048576 {
		t.Fatalf("captured %d binary bytes", count)
	}
}

func TestSupervisorCancelAndDeadline(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "owner_eof"} {
		t.Run(mode, func(t *testing.T) {
			dir := fixture(t, fixtureConfig{Code: `sleep 60 & echo $! > child.pid; echo $$ > leader.pid; wait`})
			start := fixtureStartup(dir)
			if mode == "deadline" {
				start.Deadline = time.Now().Add(500 * time.Millisecond)
			}
			h, _ := startFixture(t, dir, start)
			pidBytes := waitFile(t, filepath.Join(dir, "child.pid"))
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" {
				if err := h.Cancel(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "owner_eof" {
				h.input.Close()
			}
			outcome, err := waitResult(t, h)
			want := map[string]string{"cancel": "cancelled", "deadline": "deadline", "owner_eof": "owner_lost"}[mode]
			if err != nil || outcome.Reason != want || !outcome.Result.Retired || outcome.Result.Signal != 9 {
				t.Fatalf("outcome: %+v, %v", outcome, err)
			}
			if err := unix.Kill(pid, 0); err != unix.ESRCH {
				t.Fatalf("descendant remains: %v", err)
			}
			assertProof(t, dir, "retired")
		})
	}
}

func TestSupervisorRetiresEscapedDescendants(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid unavailable")
	}
	dir := fixture(t, fixtureConfig{Code: `setsid sh -c 'echo $$ > escaped.pid; sleep 60' &
while [ ! -s escaped.pid ]; do sleep .01; done
exit 0`})
	h, _ := startFixture(t, dir, fixtureStartup(dir))
	pid, err := strconv.Atoi(strings.TrimSpace(string(waitFile(t, filepath.Join(dir, "escaped.pid")))))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := waitResult(t, h)
	if err != nil || !outcome.Result.Retired || outcome.Result.ExitCode == nil || *outcome.Result.ExitCode != 0 {
		t.Fatalf("outcome: %+v, %v", outcome, err)
	}
	if err := unix.Kill(pid, 0); err != unix.ESRCH {
		t.Fatalf("escaped descendant remains: %v", err)
	}
}

func TestSupervisorCaptureAndPersistenceFailures(t *testing.T) {
	for _, mode := range []string{"append", "finish", "pending", "queue"} {
		t.Run(mode, func(t *testing.T) {
			code := `printf output; printf final > command-finished`
			if mode == "queue" {
				code = `head -c 4194304 /dev/zero; printf final > command-finished`
			}
			dir := fixture(t, fixtureConfig{Code: code, AppendDelay: map[bool]time.Duration{true: 100 * time.Millisecond}[mode == "queue"], AppendFail: mode == "append", FinishFail: mode == "finish", PendingFail: mode == "pending"})
			h, _ := startFixture(t, dir, fixtureStartup(dir))
			outcome, err := waitResult(t, h)
			if mode == "pending" {
				if err == nil {
					t.Fatal("pending failure accepted")
				}
				if _, err := os.Stat(filepath.Join(dir, "command-finished")); !os.IsNotExist(err) {
					t.Fatal("spawned without pending proof")
				}
				assertProof(t, dir, "not_spawned")
				return
			}
			if err != nil || !outcome.Result.Retired {
				t.Fatalf("outcome: %+v, %v", outcome, err)
			}
			waitFile(t, filepath.Join(dir, "command-finished"))
			if mode == "append" || mode == "queue" {
				if !outcome.Result.CaptureLost {
					t.Fatal("capture loss hidden")
				}
				assertProof(t, dir, "retired")
			}
			if mode == "finish" {
				if outcome.TerminalPersisted {
					t.Fatal("failed finalization certified")
				}
				assertProof(t, dir, "pending")
			}
		})
	}
}

func probeLease(t *testing.T, dir, name string, blocked bool) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if blocked && err != unix.EWOULDBLOCK {
		t.Fatalf("%s lease released early: %v", name, err)
	}
	if !blocked && err != nil {
		t.Fatalf("%s lease still held: %v", name, err)
	}
}

func reapOrphan(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		n, err := unix.Wait4(pid, nil, unix.WNOHANG, nil)
		if n == pid || err == unix.ECHILD {
			return
		}
		if err != nil && err != unix.EINTR {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("orphan helper was not reaped")
}

func TestOwnerSIGKILLRetainsLeasesThroughHelperClose(t *testing.T) {
	dir := fixture(t, fixtureConfig{Code: `sleep 60 & echo $! > child.pid; wait`, HoldClose: true})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	owner := exec.Command(exe, "fixture-owner")
	owner.Env = append(os.Environ(), "CAPY_SUPERVISOR_FIXTURE="+dir)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Process.Kill() })
	helperPID, err := strconv.Atoi(string(waitFile(t, filepath.Join(dir, "helper.pid"))))
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(waitFile(t, filepath.Join(dir, "child.pid")))))
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Kill(helperPID, unix.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	helperReaped := false
	t.Cleanup(func() {
		if !helperReaped {
			unix.Kill(helperPID, unix.SIGCONT)
			os.WriteFile(filepath.Join(dir, "release"), nil, 0o600)
		}
	})
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Wait(); err == nil {
		t.Fatal("owner survived SIGKILL")
	}
	for _, name := range []string{"owner.lock", "lifecycle.lock"} {
		probeLease(t, dir, name, true)
	}
	assertProof(t, dir, "pending")
	if err := unix.Kill(childPID, 0); err != nil {
		t.Fatalf("fixture child exited before delayed cleanup: %v", err)
	}
	if err := unix.Kill(helperPID, unix.SIGCONT); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(dir, "closing"))
	assertProof(t, dir, "retired")
	if err := unix.Kill(childPID, 0); err != unix.ESRCH {
		t.Fatalf("owner loss left a descendant: %v", err)
	}
	for _, name := range []string{"owner.lock", "lifecycle.lock"} {
		probeLease(t, dir, name, true)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	reapOrphan(t, helperPID)
	helperReaped = true
	for _, name := range []string{"owner.lock", "lifecycle.lock"} {
		probeLease(t, dir, name, false)
	}
}

func TestSupervisorLossNeverCertifiesQuiescence(t *testing.T) {
	dir := fixture(t, fixtureConfig{Code: `echo $$ > leader.pid; exec sleep 60`})
	h, leases := startFixture(t, dir, fixtureStartup(dir))
	leader, err := strconv.Atoi(strings.TrimSpace(string(waitFile(t, filepath.Join(dir, "leader.pid")))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Kill(leader, unix.SIGKILL); reapOrphan(t, leader) })
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	outcome, err := waitResult(t, h)
	if err == nil || outcome.Result.Retired || outcome.TerminalPersisted || outcome.Reason != "cleanup_unverified" {
		t.Fatalf("lost helper certified: %+v %v", outcome, err)
	}
	leases.Owner.Close()
	leases.Lifecycle.Close()
	for _, name := range []string{"owner.lock", "lifecycle.lock"} {
		probeLease(t, dir, name, false)
	}
	assertProof(t, dir, "pending")
	// Free locks alone cannot authorize reset/reuse: a new opener must inspect
	// durable pending evidence. This fixture backend refuses to reopen it.
	if _, err := openFixture(context.Background(), fixtureStartup(dir), fixtureKey, Leases{}); err == nil {
		t.Fatal("pending generation reused after helper loss")
	}
	if err := unix.Kill(leader, 0); err != nil {
		t.Fatalf("forced helper-loss limit was not exercised: %v", err)
	}
}

func TestSupervisorCompilerPhases(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			dir := fixture(t, fixtureConfig{Language: executor.Rust, Code: "synthetic compiler input"})
			compiler := "#!/bin/sh\nprintf compiler >&2\n"
			switch mode {
			case "failure":
				compiler += "exit 23\n"
			case "cancel":
				compiler += "sleep 60 & echo $! > \"" + dir + "/compiler-child.pid\"; wait\n"
			default:
				compiler += "printf '#!/bin/sh\\nprintf runtime\\n' > \"$3\"\nchmod +x \"$3\"\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "rustc"), []byte(compiler), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			h, _ := startFixture(t, dir, fixtureStartup(dir))
			pid := 0
			if mode == "cancel" {
				var err error
				pid, err = strconv.Atoi(strings.TrimSpace(string(waitFile(t, filepath.Join(dir, "compiler-child.pid")))))
				if err != nil {
					t.Fatal(err)
				}
				if err := h.Cancel(); err != nil {
					t.Fatal(err)
				}
			}
			outcome, err := waitResult(t, h)
			if err != nil || !outcome.Result.Retired || !outcome.Result.Started {
				t.Fatalf("outcome: %+v %v", outcome, err)
			}
			events := readEvents(t, dir)
			if len(events) == 0 || events[0].Phase != "compile" || string(events[0].Data) != "compiler" {
				t.Fatalf("compile output missing: %+v", events)
			}
			if mode == "success" && (len(events) != 2 || events[1].Phase != "run" || string(events[1].Data) != "runtime") {
				t.Fatalf("runtime output missing: %+v", events)
			}
			if mode == "failure" && (outcome.Result.ExitCode == nil || *outcome.Result.ExitCode != 23 || len(events) != 1) {
				t.Fatalf("compiler failure hidden: %+v", outcome)
			}
			if mode == "cancel" && (outcome.Reason != "cancelled" || unix.Kill(pid, 0) != unix.ESRCH) {
				t.Fatalf("compiler cancellation failed: %+v", outcome)
			}
			assertProof(t, dir, "retired")
		})
	}
}

func TestSupervisorDeadlineDuringCaptureFlushPreservesExit(t *testing.T) {
	dir := fixture(t, fixtureConfig{Code: `printf finished; printf final > command-finished`, AppendDelay: 1500 * time.Millisecond})
	start := fixtureStartup(dir)
	start.Deadline = time.Now().Add(time.Second)
	h, _ := startFixture(t, dir, start)
	outcome, err := waitResult(t, h)
	if err != nil || outcome.Reason != "" || outcome.Result.ExitCode == nil || *outcome.Result.ExitCode != 0 {
		t.Fatalf("storage flush changed execution outcome: %+v %v", outcome, err)
	}
	if !outcome.TerminalPersisted {
		t.Fatal("terminal was not persisted")
	}
}

func TestSupervisorNaturalExitWinsLateCancellation(t *testing.T) {
	// Hold the post-spawn callback until the command has exited, then send
	// cancellation before Waitid can observe that already-finished process.
	dir := fixture(t, fixtureConfig{Code: `printf final > command-finished; exit 7`, HoldStarted: true})
	h, _ := startFixture(t, dir, fixtureStartup(dir))
	waitFile(t, filepath.Join(dir, "command-finished"))
	if err := h.Cancel(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "release-started"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	outcome, err := waitResult(t, h)
	if err != nil || outcome.Reason != "" || outcome.Result.ExitCode == nil || *outcome.Result.ExitCode != 7 {
		t.Fatalf("late cancellation rewrote natural exit: %+v %v", outcome, err)
	}
}
