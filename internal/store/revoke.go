package store

import (
	"context"
	"database/sql"
	"fmt"
)

// RevokeAccess signs everyone out of a freshly restored database: it deletes
// every session, every connection token and every pairing still in progress
// (a pending peer row). A backup holds the sessions and tokens of the moment
// it was taken, and restoring it must not revive one that was revoked since.
// Established peers keep their credentials, so pairings survive. db must be
// migrated (Open). It is idempotent.
func RevokeAccess(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("revoke restored access: %w", err)
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DELETE FROM sessions`,
		`DELETE FROM connection_tokens`,
		`DELETE FROM peers WHERE state = 'pending'`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("revoke restored access: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("revoke restored access: %w", err)
	}
	return nil
}
