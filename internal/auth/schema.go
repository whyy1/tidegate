package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// Tables owned by this package. All names carry the auth_ prefix so they
// can share a database with other packages.
//
// auth_meta keeps this package's schema version under key "schema_version".
// Timestamps are Unix milliseconds (UTC).
var migrations = [][]string{
	// Version 1: users and sessions.
	{
		`CREATE TABLE IF NOT EXISTS auth_users (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			username            TEXT    NOT NULL COLLATE NOCASE UNIQUE,
			password_hash       TEXT    NOT NULL,
			role                TEXT    NOT NULL DEFAULT 'admin',
			created_at          INTEGER NOT NULL,
			updated_at          INTEGER NOT NULL,
			password_changed_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS auth_sessions (
			id           TEXT    PRIMARY KEY,
			token_hash   BLOB    NOT NULL UNIQUE,
			user_id      INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
			created_at   INTEGER NOT NULL,
			last_seen_at INTEGER NOT NULL,
			expires_at   INTEGER NOT NULL,
			remote_addr  TEXT    NOT NULL DEFAULT '',
			user_agent   TEXT    NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS auth_sessions_user_idx ON auth_sessions(user_id)`,
		`CREATE INDEX IF NOT EXISTS auth_sessions_expires_idx ON auth_sessions(expires_at)`,
	},
}

const schemaVersionKey = "schema_version"

// Migrate creates or upgrades the tables owned by this package. It is safe
// to call on every start; already applied steps are skipped. It fails if the
// database was migrated by a newer version of this package.
func Migrate(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("auth: migrate: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS auth_meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("auth: migrate: create auth_meta: %w", err)
	}

	current := 0
	var raw string
	switch err = tx.QueryRowContext(ctx, `SELECT value FROM auth_meta WHERE key = ?`, schemaVersionKey).Scan(&raw); {
	case errors.Is(err, sql.ErrNoRows):
		err = nil
	case err != nil:
		return fmt.Errorf("auth: migrate: read schema version: %w", err)
	default:
		if current, err = strconv.Atoi(raw); err != nil || current < 0 {
			return fmt.Errorf("auth: migrate: invalid schema version %q", raw)
		}
	}
	if current > len(migrations) {
		return fmt.Errorf("auth: migrate: database schema version %d is newer than supported version %d", current, len(migrations))
	}
	if current == len(migrations) {
		return tx.Commit()
	}

	for v := current; v < len(migrations); v++ {
		for _, stmt := range migrations[v] {
			if _, err = tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("auth: migrate to version %d: %w", v+1, err)
			}
		}
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO auth_meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		schemaVersionKey, strconv.Itoa(len(migrations))); err != nil {
		return fmt.Errorf("auth: migrate: write schema version: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("auth: migrate: commit: %w", err)
	}
	return nil
}
