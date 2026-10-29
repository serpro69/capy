//go:build cgo && (linux || darwin)

package runstore

/*
// SQLite's public version-1 VFS prefix, from the pinned SQLite 3.51.2
// sqlite3-binding.h (public domain). Declaring only the stable prefix avoids
// depending on a separately installed system SQLite header/library. Symbols
// resolve to the same bundled SQLite already linked by go-sqlite3.
#include <stddef.h>
#include <string.h>
typedef struct sqlite3_file sqlite3_file;
struct sqlite3_file { const void *pMethods; };
typedef struct sqlite3_vfs sqlite3_vfs;
struct sqlite3_vfs {
  int iVersion, szOsFile, mxPathname;
  sqlite3_vfs *pNext;
  const char *zName;
  void *pAppData;
  int (*xOpen)(sqlite3_vfs*, const char*, sqlite3_file*, int, int*);
  int (*xDelete)(sqlite3_vfs*, const char*, int);
  int (*xAccess)(sqlite3_vfs*, const char*, int, int*);
  int (*xFullPathname)(sqlite3_vfs*, const char*, int, char*);
  void *(*xDlOpen)(sqlite3_vfs*, const char*);
  void (*xDlError)(sqlite3_vfs*, int, char*);
  void (*(*xDlSym)(sqlite3_vfs*, void*, const char*))(void);
  void (*xDlClose)(sqlite3_vfs*, void*);
  int (*xRandomness)(sqlite3_vfs*, int, char*);
  int (*xSleep)(sqlite3_vfs*, int);
  int (*xCurrentTime)(sqlite3_vfs*, double*);
  int (*xGetLastError)(sqlite3_vfs*, int, char*);
};
extern sqlite3_vfs *sqlite3_vfs_find(const char*);
extern int sqlite3_vfs_register(sqlite3_vfs*, int);
extern int sqlite3_uri_boolean(const char*, const char*, int);

static sqlite3_vfs capy_inspection_vfs;
static sqlite3_vfs *capy_inspection_base;

static int capy_inspection_open(sqlite3_vfs *vfs, const char *name,
                                sqlite3_file *file, int flags, int *out) {
  // MAIN_DB=0x100, WAL=0x80000. Never open journals or disk temp files.
  file->pMethods = NULL;
  if (!name || !(flags & (0x100 | 0x80000))) return 14; // CANTOPEN
  // The unix VFS opens SHM internally, outside xOpen. Require its explicit
  // read-only option on every main DB; absent SHM then fails without creation.
  if ((flags & 0x100) && !sqlite3_uri_boolean(name, "readonly_shm", 0)) return 14;
  flags = (flags & ~(0x2 | 0x4 | 0x8 | 0x10)) | 0x1; // READONLY, no CREATE
  return capy_inspection_base->xOpen(capy_inspection_base, name, file, flags, out);
}
static int capy_inspection_delete(sqlite3_vfs *vfs, const char *name, int sync) {
  return 8; // READONLY: even malformed history cannot trigger file deletion.
}
// Forward with the original VFS pointer: the encrypted default VFS has private
// state beyond the public prefix and must not receive our smaller wrapper.
static int capy_inspection_access(sqlite3_vfs *v, const char *n, int f, int *r) {
  return capy_inspection_base->xAccess(capy_inspection_base,n,f,r);
}
static int capy_inspection_path(sqlite3_vfs *v, const char *n, int s, char *r) {
  return capy_inspection_base->xFullPathname(capy_inspection_base,n,s,r);
}
static void *capy_inspection_dlopen(sqlite3_vfs *v, const char *n) {
  return capy_inspection_base->xDlOpen(capy_inspection_base,n);
}
static void capy_inspection_dlerror(sqlite3_vfs *v, int s, char *r) {
  capy_inspection_base->xDlError(capy_inspection_base,s,r);
}
static void (*capy_inspection_dlsym(sqlite3_vfs *v, void *h, const char *n))(void) {
  return capy_inspection_base->xDlSym(capy_inspection_base,h,n);
}
static void capy_inspection_dlclose(sqlite3_vfs *v, void *h) {
  capy_inspection_base->xDlClose(capy_inspection_base,h);
}
static int capy_inspection_random(sqlite3_vfs *v, int s, char *r) {
  return capy_inspection_base->xRandomness(capy_inspection_base,s,r);
}
static int capy_inspection_sleep(sqlite3_vfs *v, int n) {
  return capy_inspection_base->xSleep(capy_inspection_base,n);
}
static int capy_inspection_time(sqlite3_vfs *v, double *r) {
  return capy_inspection_base->xCurrentTime(capy_inspection_base,r);
}
static int capy_inspection_error(sqlite3_vfs *v, int s, char *r) {
  return capy_inspection_base->xGetLastError ? capy_inspection_base->xGetLastError(capy_inspection_base,s,r) : 0;
}
static int capy_register_inspection_vfs(void) {
  capy_inspection_base = sqlite3_vfs_find(NULL);
  if (!capy_inspection_base || capy_inspection_base->iVersion < 1) return 14;
  memcpy(&capy_inspection_vfs, capy_inspection_base, sizeof(capy_inspection_vfs));
  capy_inspection_vfs.iVersion = 1;
  capy_inspection_vfs.pNext = NULL;
  capy_inspection_vfs.zName = "capy-inspection";
  capy_inspection_vfs.xOpen = capy_inspection_open;
  capy_inspection_vfs.xDelete = capy_inspection_delete;
  capy_inspection_vfs.xAccess = capy_inspection_access;
  capy_inspection_vfs.xFullPathname = capy_inspection_path;
  capy_inspection_vfs.xDlOpen = capy_inspection_dlopen;
  capy_inspection_vfs.xDlError = capy_inspection_dlerror;
  capy_inspection_vfs.xDlSym = capy_inspection_dlsym;
  capy_inspection_vfs.xDlClose = capy_inspection_dlclose;
  capy_inspection_vfs.xRandomness = capy_inspection_random;
  capy_inspection_vfs.xSleep = capy_inspection_sleep;
  capy_inspection_vfs.xCurrentTime = capy_inspection_time;
  capy_inspection_vfs.xGetLastError = capy_inspection_error;
  return sqlite3_vfs_register(&capy_inspection_vfs, 0);
}
*/
import "C"

import "sync"

var inspectionVFS = sync.OnceValue(func() error {
	if C.capy_register_inspection_vfs() != 0 {
		return storageError()
	}
	return nil
})

func inspectionSQLiteOptions() (string, error) {
	if err := inspectionVFS(); err != nil {
		return "", err
	}
	return "&mode=ro&vfs=capy-inspection&readonly_shm=1&_busy_timeout=100", nil
}
