//go:build integration

package runstore

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestLeaseRefsInheritedHelper(t *testing.T) {
	if os.Getenv("CAPY_LEASE_REFS_FIXTURE") != "1" {
		return
	}
	for _, fd := range []uintptr{3, 4} {
		f := os.NewFile(fd, "inherited-lease")
		if _, err := f.Stat(); err != nil {
			t.Fatal(err)
		}
		defer f.Close()
	}
	if _, err := os.Stdout.Write([]byte("ready")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseRefsInheritancePreservesLocksAndPermanentFiles(t *testing.T) {
	s, o := leaseRefsFixture(t)
	refs := borrowForTest(t, s, o)
	paths := []string{s.OwnerLeasePath(o.Slot), filepath.Join(filepath.Dir(s.Path()), "lifecycle.lock")}
	before := make([]os.FileInfo, len(paths))
	for i, path := range paths {
		var err error
		before[i], err = os.Stat(path)
		require.NoError(t, err)
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(executable, "-test.run=^TestLeaseRefsInheritedHelper$")
	cmd.Env = append(os.Environ(), "CAPY_LEASE_REFS_FIXTURE=1")
	inputR, inputW, err := os.Pipe()
	require.NoError(t, err)
	defer inputR.Close()
	defer inputW.Close()
	outputR, outputW, err := os.Pipe()
	require.NoError(t, err)
	defer outputR.Close()
	defer outputW.Close()
	cmd.Stdin, cmd.Stdout = inputR, outputW
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, refs.WithFiles(s.Path(), o.ID, func(owner, life *os.File) error {
		cmd.ExtraFiles = []*os.File{owner, life}
		err := cmd.Start()
		cmd.ExtraFiles = nil
		return err
	}))
	joined := false
	defer func() {
		if !joined {
			// Direct fixture child; no command process is spawned by this helper.
			if err := cmd.Process.Kill(); err != nil {
				t.Logf("fixture kill: %v", err)
			}
			if err := cmd.Wait(); err != nil {
				t.Logf("fixture cleanup wait: %v", err)
			}
		}
	}()
	require.NoError(t, inputR.Close())
	require.NoError(t, outputW.Close())
	require.NoError(t, outputR.SetReadDeadline(time.Now().Add(5*time.Second)))
	ready := make([]byte, 5)
	_, err = io.ReadFull(outputR, ready)
	require.NoError(t, err)
	require.Equal(t, "ready", string(ready))
	// Only the helper now owns kernel references. Closing the local references
	// must never issue LOCK_UN or convert either inherited lock.
	require.NoError(t, refs.Close())
	require.NoError(t, o.Lease.Close())
	require.NoError(t, s.Close())
	assertLeaseBusy(t, paths[0], unix.LOCK_SH, true)
	assertLeaseBusy(t, paths[0], unix.LOCK_EX, true)
	assertLeaseBusy(t, paths[1], unix.LOCK_SH, false)
	assertLeaseBusy(t, paths[1], unix.LOCK_EX, true)
	var resetErr *Error
	require.ErrorAs(t, Reset(t.Context(), s.Path()), &resetErr)
	require.Equal(t, "storage_busy", resetErr.Code)
	require.NoError(t, inputW.Close())
	// EOF is the child's sole release instruction; observe exit without adding
	// another asynchronous waiter or exercising the future exchange protocol.
	require.NoError(t, outputR.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = io.Copy(io.Discard, outputR)
	require.NoError(t, err)
	err = cmd.Wait()
	joined = true
	require.NoError(t, err, stderr.String())
	assertLeaseBusy(t, paths[0], unix.LOCK_EX, false)
	assertLeaseBusy(t, paths[1], unix.LOCK_EX, false)
	for i, path := range paths {
		after, err := os.Stat(path)
		require.NoError(t, err)
		require.True(t, os.SameFile(before[i], after))
		require.Equal(t, before[i].Mode(), after.Mode())
		contents, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Empty(t, contents)
	}
	require.NoError(t, Reset(t.Context(), s.Path()))
	for i, path := range paths {
		after, err := os.Stat(path)
		require.NoError(t, err)
		require.True(t, os.SameFile(before[i], after), "reset preserves permanent locks")
	}
}
