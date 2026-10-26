package execution

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/serpro69/capy/internal/executor"
)

func supervisorCleanupScope() executor.CleanupScope { return executor.CleanupLinuxDescendants }

func ServeInherited(ctx context.Context, open OpenRun) error {
	return serveInherited(ctx, open, supervisorCleanupScope())
}

func validateLeases(owner, lifecycle *os.File) error {
	a, err := owner.Stat()
	if err != nil {
		return errors.New("invalid owner lease")
	}
	b, err := lifecycle.Stat()
	if err != nil || os.SameFile(a, b) {
		return errors.New("invalid lifecycle lease")
	}
	for i, f := range []*os.File{owner, lifecycle} {
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return errors.New("unsafe supervisor lease")
		}
		mode := "READ"
		if i == 0 {
			mode = "WRITE"
		}
		if err := verifyDescriptorLock(f, mode); err != nil {
			return err
		}
	}
	return nil
}

// Linux show_fd_locks filters flock entries by their struct file, so fdinfo
// proves this open-file description owns the lock. Probing a new shared flock
// cannot prove inheritance: an unrelated shared holder would also allow it.
// Never acquire/convert/unlock a lease while validating its provenance.
func verifyDescriptorLock(f *os.File, mode string) error {
	info, err := os.Open("/proc/self/fdinfo/" + strconv.Itoa(int(f.Fd())))
	if err != nil {
		return errors.New("supervisor lock information unavailable")
	}
	defer info.Close()
	b, err := io.ReadAll(io.LimitReader(info, 4097))
	if err != nil || len(b) > 4096 {
		return errors.New("supervisor lock information exceeded")
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 9 && fields[0] == "lock:" && fields[2] == "FLOCK" && fields[3] == "ADVISORY" && fields[4] == mode && fields[7] == "0" && fields[8] == "EOF" {
			return nil
		}
	}
	return errors.New("supervisor descriptor does not own the required lease")
}
