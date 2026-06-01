package vault

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVaultStore_Stats(t *testing.T) {
	s := newTestVault(t)

	r1 := sampleRecord("aaaaaaaa-1111-0000-0000-000000000000")
	r1.Session.ProjectPath = "/home/user/proj-a"
	r1.Session.SizeBytes = 1000
	r1.Session.StartTime = time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	r1.Session.EndTime = time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC)
	require.NoError(t, s.InsertSession(r1))

	r2 := sampleRecord("bbbbbbbb-2222-0000-0000-000000000000")
	r2.Session.ProjectPath = "/home/user/proj-b"
	r2.Session.SizeBytes = 500
	r2.Session.StartTime = time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	r2.Session.EndTime = time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	require.NoError(t, s.InsertSession(r2))

	r3 := sampleRecord("cccccccc-3333-0000-0000-000000000000")
	r3.Session.ProjectPath = "/home/user/proj-a" // same project as r1
	r3.Session.SizeBytes = 300
	require.NoError(t, s.InsertSession(r3))

	st, err := s.Stats()
	require.NoError(t, err)

	assert.Equal(t, 3, st.Sessions)
	assert.Equal(t, int64(1800), st.TotalBytes, "summed size_bytes")
	assert.True(t, time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC).Equal(st.Oldest), "min start_time")
	assert.True(t, time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC).Equal(st.Newest), "max end_time")

	require.Len(t, st.ByProject, 2)
	assert.Equal(t, "/home/user/proj-a", st.ByProject[0].ProjectPath, "busiest project first")
	assert.Equal(t, 2, st.ByProject[0].Count)
	assert.Equal(t, "/home/user/proj-b", st.ByProject[1].ProjectPath)
	assert.Equal(t, 1, st.ByProject[1].Count)
}

func TestVaultStore_StatsEmpty(t *testing.T) {
	s := newTestVault(t)
	st, err := s.Stats()
	require.NoError(t, err)
	assert.Equal(t, 0, st.Sessions)
	assert.Equal(t, int64(0), st.TotalBytes)
	assert.True(t, st.Oldest.IsZero())
	assert.True(t, st.Newest.IsZero())
	assert.Empty(t, st.ByProject)
}
