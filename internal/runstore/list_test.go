package runstore

import (
	"database/sql"
	"encoding/base64"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/stretchr/testify/require"
)

func TestListCutoffFiltersAndRecovery(t *testing.T) {
	path, err := config.ResolveRunDBPath(t.TempDir())
	require.NoError(t, err)
	st, err := Open(t.Context(), path, testKey, true)
	require.NoError(t, err)
	defer st.Close()
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer owner.Lease.Close()
	admit := func() string {
		row, err := st.Admit(t.Context(), owner, Program{OwnerType: "cli", TTLHours: 24, MaxRunBytes: 100})
		require.NoError(t, err)
		return row.ID
	}
	first, second := admit(), admit()
	page, err := st.List(t.Context(), ListOptions{Limit: 1, States: []string{"admitted"}})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, first, page.Entries[0].RunID)
	require.Equal(t, "cli", page.Entries[0].OwnerType)
	require.NotNil(t, page.NextCursor)
	third := admit()
	next, err := st.List(t.Context(), ListOptions{Limit: 1, Cursor: *page.NextCursor})
	require.NoError(t, err)
	require.Len(t, next.Entries, 1)
	require.Equal(t, second, next.Entries[0].RunID)
	require.Nil(t, next.NextCursor)
	all, err := st.List(t.Context(), ListOptions{Limit: 64})
	require.NoError(t, err)
	require.Len(t, all.Entries, 3)
	require.Equal(t, third, all.Entries[2].RunID)
	for _, options := range []ListOptions{{Limit: 0}, {Limit: 65}, {Limit: 1, States: []string{"unknown"}}, {Limit: 1, Cursor: base64.RawURLEncoding.EncodeToString([]byte(`{}`))}, {Limit: 1, Cursor: *page.NextCursor, States: []string{"exited"}}} {
		_, err := st.List(t.Context(), options)
		require.Error(t, err)
	}
	row, err := st.Load(t.Context(), first)
	require.NoError(t, err)
	expired := time.Now().Add(-time.Hour)
	row.Result.ExpiresAt = &expired
	require.NoError(t, st.update(t.Context(), first, func(_ *sql.Tx, current *Admitted) error { current.Result.ExpiresAt = &expired; return nil }))
	all, err = st.List(t.Context(), ListOptions{Limit: 64})
	require.NoError(t, err)
	require.Len(t, all.Entries, 2)
}
