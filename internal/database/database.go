// Package database provides SQLite connections and schema migrations.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"
)

// Open opens a SQLite database and applies pending migrations. Its parent directory must exist.
// The caller is responsible for closing the returned database.
func Open(ctx context.Context, dbPath string) (*sql.DB, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("database path is empty")
	}

	dsn := "file:" + url.PathEscape(dbPath) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := runMigrations(ctx, db, schemaMigrations); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return db, nil
}
