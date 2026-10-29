package runstore

import (
	"context"
	"errors"

	"github.com/serpro69/capy/internal/sqliteutil"
)

func attributionUnavailable(err error) bool {
	var typed *Error
	if !errors.As(err, &typed) {
		return false
	}
	switch typed.Code {
	case "recovery_capacity", "recovery_revision_exhausted", "incident_publication_failed":
		return true
	}
	return false
}

// A configured query may still recover retained bytes when new attribution
// cannot be committed. Keep the acquired lifecycle lease, replace the connection
// with mode=ro (including all future pool connections), and retain an immutable
// admission refusal. Helpers use Open, so they never receive this fallback.
func (s *Store) attachRecoveryReader(ctx context.Context, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.db.Close(); err != nil {
		return storageError()
	}
	db, err := sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(s.path, s.key)+"&mode=ro&_busy_timeout=100", s.path, "selected run credential")
	if err != nil {
		return openError(err, s.path)
	}
	s.db = db
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var id string
	var version int
	if err := db.QueryRowContext(ctx, "SELECT store_id,version FROM run_meta WHERE singleton=1").Scan(&id, &version); err != nil || id != s.id || version != schemaVersion {
		return storageError()
	}
	s.recoveryReadOnly = cause
	return nil
}
