package server

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 6 (A6): the server owns one long-lived VaultStore, shared by the startup
// sweep and (from Tasks 7/8) the search handlers, and Close()d once in shutdown.
// These tests assert the three guarantees: a shared handle (no per-call open),
// concurrent read + sweep safety, and a WAL checkpoint on shutdown.

// getVault returns the same handle across calls, so the search path never opens
// a per-call VaultStore. Without a key it returns nil (vault disabled), letting
// callers degrade loudly.
func TestGetVault_SharedHandleAndDisabled(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)

	t.Run("nil without key", func(t *testing.T) {
		t.Setenv("CAPY_VAULT_KEY", "") // vault is opt-in
		srv := newTestServerWithProjectDir(t, nil, projectDir)
		require.Nil(t, srv.getVault(), "vault must be disabled (nil handle) without CAPY_VAULT_KEY")
	})

	t.Run("stable handle with key", func(t *testing.T) {
		t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
		srv := newTestServerWithProjectDir(t, nil, projectDir)
		v1 := srv.getVault()
		v2 := srv.getVault()
		require.NotNil(t, v1)
		require.Same(t, v1, v2, "getVault must return one long-lived handle, not a per-call open")
	})
}

// A concurrent startup sweep (writer) and search reads (readers) share one
// handle without a data race or error. Run under `make test-race` this proves
// the shared-handle pattern is safe; here it also asserts the swept data is
// searchable through the same handle afterward.
func TestVault_ConcurrentSweepAndRead(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)

	// Open eagerly so the reader goroutine has a live DB to hit before the sweep
	// commits — exercising reads concurrent with the sweep's writes, not just
	// reads after it.
	require.NoError(t, srv.getVault().Open(context.Background()))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		srv.vaultSweep(context.Background())
	}()
	go func() {
		defer wg.Done()
		for range 25 {
			// Both flavors read the shared handle: chunk search (Task 5) and the
			// per-line search. Early hits may be empty (sweep not committed yet);
			// we only require no error / no race. Use assert (not require) inside
			// the goroutine: require's t.FailNow calls runtime.Goexit, which must
			// run on the test goroutine only — assert's t.Errorf is goroutine-safe.
			_, err := srv.getVault().SearchChunks(context.Background(), vault.SearchOptions{Query: "database"})
			assert.NoError(t, err)
			_, err = srv.getVault().Search(context.Background(), vault.SearchOptions{Query: "database"})
			assert.NoError(t, err)
		}
	}()
	wg.Wait()

	// After the sweep, the archived sessions are searchable through the very same
	// handle the readers used — no reopen needed.
	sessions, err := srv.getVault().ListSessions(context.Background(), vault.ListOptions{})
	require.NoError(t, err)
	require.Len(t, sessions, 2, "both fixture sessions should be archived by the sweep")

	hits, err := srv.getVault().SearchChunks(context.Background(), vault.SearchOptions{Query: "database"})
	require.NoError(t, err)
	assert.NotEmpty(t, hits, "swept content must be chunk-searchable via the shared handle")
}

// shutdown() Close()s the server-owned handle, which checkpoints the WAL into
// the main DB file. After shutdown vault.db-wal must be truncated to zero (or
// removed) and the swept data must be durable in the main file.
func TestVaultShutdown_CheckpointsWAL(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background()) // opens the handle + writes → WAL grows

	dbPath := vault.VaultDBPath()
	walPath := dbPath + "-wal"

	srv.shutdown() // Close → WAL checkpoint(TRUNCATE)

	if fi, err := os.Stat(walPath); err == nil {
		assert.Zero(t, fi.Size(), "vault.db-wal must be checkpointed (truncated) on shutdown")
	}

	// Data is durable in the main DB file: a fresh independent handle sees both
	// sessions (proving the checkpoint flushed the WAL, not just deleted it).
	st := vault.NewVaultStore(dbPath)
	t.Cleanup(func() { _ = st.Close() })
	sessions, err := st.ListSessions(context.Background(), vault.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, sessions, 2, "swept sessions must survive shutdown in the main DB file")
}
