package runstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Reset discards only the selected owner's disposable run database and known
// SQLite sidecars. The caller must require explicit --discard. It never resolves
// credentials, opens SQLite, signals recorded PIDs or removes permanent locks.
// Operators must stop old command groups first when cleanup was unverified.
func Reset(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) || filepath.Base(path) != "runs.db" || filepath.Base(filepath.Dir(path)) != "runs" || filepath.Base(filepath.Dir(filepath.Dir(path))) != ".capy" {
		return storageError()
	}
	owner := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	physical, err := filepath.EvalSymlinks(owner)
	if err != nil || physical != owner {
		return storageError()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := inspectStore(path); err != nil {
		return err
	}
	root := filepath.Dir(path)
	rootInfo, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return storageError()
	}
	// Anchor deletion to the validated directory descriptor. A later rename
	// or parent symlink replacement must not redirect an explicit discard.
	dir, err := os.OpenRoot(root)
	if err != nil {
		return storageError()
	}
	defer dir.Close()
	openedInfo, err := dir.Stat(".")
	if err != nil || !os.SameFile(rootInfo, openedInfo) {
		return storageError()
	}
	life, err := openRegular(filepath.Join(root, "lifecycle.lock"), false)
	if err != nil {
		return storageError()
	}
	defer life.Close()
	leaseInfo, err := life.Stat()
	if err != nil {
		return storageError()
	}
	anchoredInfo, err := dir.Lstat("lifecycle.lock")
	if err != nil || !safeRegular(anchoredInfo) || !os.SameFile(leaseInfo, anchoredInfo) {
		return storageError()
	}
	if err := unix.Flock(int(life.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return failure("storage_busy", "run reset refused: stop all owners, readers and supervisors; after cleanup_unverified also stop old execution groups before retrying explicit discard")
		}
		return storageError()
	}
	// Validate every known path before removing anything. A missing/unsafe
	// permanent lock is not repaired during destructive recovery.
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := dir.Lstat("runs.db" + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !safeRegular(info) {
			return storageError()
		}
	}
	locksInfo, err := dir.Lstat("locks")
	if err != nil || !locksInfo.IsDir() || locksInfo.Mode().Perm() != 0700 {
		return storageError()
	}
	lockPaths := []string{"recovery.lock"}
	registry := &Store{path: path}
	for slot := 0; slot < OwnerSlots; slot++ {
		lockPaths = append(lockPaths, filepath.Join("locks", filepath.Base(registry.OwnerLeasePath(slot))))
	}
	for _, name := range lockPaths {
		info, err := dir.Lstat(name)
		if err != nil || !safeRegular(info) {
			return storageError()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal", ""} {
		if err := dir.Remove("runs.db" + suffix); err != nil && !os.IsNotExist(err) {
			return storageError()
		}
	}
	return nil
}
