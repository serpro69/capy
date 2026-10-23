package execution

import (
	"errors"
	"os"
)

// Darwin has no fdinfo ownership proof. File safety plus the common identity
// exchange relies on the coordinator's acquisition-proven references; never
// probe or convert an inherited flock as a substitute for that provenance.
func validateLeases(owner, lifecycle *os.File) error {
	a, err := owner.Stat()
	if err != nil {
		return errors.New("invalid owner lease")
	}
	b, err := lifecycle.Stat()
	if err != nil || os.SameFile(a, b) {
		return errors.New("invalid lifecycle lease")
	}
	for _, info := range []os.FileInfo{a, b} {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return errors.New("unsafe supervisor lease")
		}
	}
	return nil
}
