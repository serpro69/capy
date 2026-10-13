package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

const codexProbeTimeout = 2 * time.Second
const codexProbeOutputLimit = 4 << 10

// CodexSetup carries a successful PATH prerequisite within one setup invocation.
// Prepare it before either platform writes in a both-platform installation.
type CodexSetup struct {
	binaryPath string
}

// PrepareCodexSetup validates the external PATH executable and optional --binary
// identity without loading project configuration or writing installation state.
func PrepareCodexSetup(ctx context.Context, binaryPath string) (*CodexSetup, error) {
	candidate, err := exec.LookPath("capy")
	if err != nil {
		return nil, fmt.Errorf("Codex requires compatible capy on PATH: %w; add the intended binary's directory to PATH and restart Codex", err)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return nil, fmt.Errorf("resolve PATH capy: %w", err)
	}
	if binaryPath != "" {
		explicit, err := exec.LookPath(binaryPath)
		if err != nil {
			return nil, fmt.Errorf("resolve --binary: %w", err)
		}
		selected, selectedErr := os.Stat(explicit)
		found, foundErr := os.Stat(candidate)
		if err := errors.Join(selectedErr, foundErr); err != nil {
			return nil, fmt.Errorf("compare --binary with PATH capy: %w", err)
		}
		if !os.SameFile(selected, found) {
			return nil, errors.New("--binary cannot override Codex's PATH launch; put that executable on PATH and restart Codex")
		}
	}
	if err := probeCodexProtocol(ctx, candidate); err != nil {
		return nil, fmt.Errorf("Codex PATH prerequisite (%s): %w; install/upgrade capy on PATH and restart Codex", candidate, err)
	}
	return &CodexSetup{binaryPath: candidate}, nil
}

// Apply writes the registration after a successful prerequisite. The result is
// deliberately invocation-local; it is not a global cache or runtime probe.
func (s *CodexSetup) Apply(projectDir string) error {
	if s == nil || s.binaryPath == "" {
		return errors.New("Codex setup requires a successful PATH prerequisite")
	}
	return setupCodex(projectDir)
}

func probeCodexProtocol(parent context.Context, candidate string) error {
	ctx, cancel := context.WithTimeout(parent, codexProbeTimeout)
	defer cancel()
	output := &codexProbeOutput{cancel: cancel}
	cmd := exec.CommandContext(ctx, candidate, "codex-launch", "--check-protocol", CodexLaunchProtocol)
	// A candidate that exits with an inherited pipe still open must not hang setup.
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Stdout = codexProbeStream{output: output}
	cmd.Stderr = codexProbeStream{output: output, stderr: true}
	err := cmd.Run() // nil stdin: probes cannot consume the caller's input.
	if output.overflow {
		return fmt.Errorf("protocol probe exceeded %d output bytes", codexProbeOutputLimit)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("protocol probe did not complete within %s: %w", codexProbeTimeout, ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("protocol probe failed: %w", err)
	}
	if output.stderr || string(output.stdout) != CodexLaunchProtocol+"\n" {
		return errors.New("unexpected protocol probe response")
	}
	return nil
}

type codexProbeOutput struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	stdout   []byte
	bytes    int
	stderr   bool
	overflow bool
}

type codexProbeStream struct {
	output *codexProbeOutput
	stderr bool
}

func (w codexProbeStream) Write(p []byte) (int, error) {
	o := w.output
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(p) > codexProbeOutputLimit-o.bytes {
		o.overflow = true
		o.cancel()
		return 0, errors.New("protocol probe output limit exceeded")
	}
	o.bytes += len(p)
	if w.stderr {
		o.stderr = o.stderr || len(p) > 0
	} else {
		o.stdout = append(o.stdout, p...)
	}
	return len(p), nil
}
