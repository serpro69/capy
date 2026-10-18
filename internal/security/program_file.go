package security

import (
	"fmt"
	"os"
	"syscall"

	"github.com/serpro69/capy/internal/fileio"
)

// ProgramFileError preserves CLI error categories without importing run storage
// into credential-free hook admission.
type ProgramFileError struct {
	Code string
	Err  error
}

func (e *ProgramFileError) Error() string { return e.Err.Error() }
func (e *ProgramFileError) Unwrap() error { return e.Err }

// ReadCLIProgram is shared by CLI admission and Codex capture inspection. Paths
// are relative to the selected owner, never the requested execution cwd. The
// policy check, nonblocking/no-follow open and bounded descriptor read must stay
// together; neither consumer opens a database or resolves credentials here.
func ReadCLIProgram(project, path string) (string, error) {
	policy, err := LoadReadPolicy(FilePolicyContext{ProjectDir: project})
	if err != nil {
		return "", &ProgramFileError{Code: "permission_denied", Err: err}
	}
	physical, err := policy.ResolveCLIProgramFile(path)
	if err != nil {
		return "", &ProgramFileError{Code: "permission_denied", Err: err}
	}
	f, err := os.OpenFile(physical, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", &ProgramFileError{Code: "invalid_argument", Err: fmt.Errorf("code-file cannot be opened as a regular file")}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxShellInputBytes {
		return "", &ProgramFileError{Code: "invalid_argument", Err: fmt.Errorf("code-file must be a regular file of at most 1 MiB")}
	}
	data, err := fileio.ReadAll(f, MaxShellInputBytes)
	if err != nil {
		return "", fmt.Errorf("read checked code-file: %w", err)
	}
	return string(data), nil
}
