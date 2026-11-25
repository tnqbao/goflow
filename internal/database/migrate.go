package database

import (
	"context"
	"database/sql"
	"fmt"
)

type migration struct {
	version int
	sql     string
}

var schemaMigrations = []migration{
	{
		version: 1,
		sql: `CREATE TABLE workflows (
			id TEXT PRIMARY KEY,
			prompt TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('queued', 'running', 'succeeded', 'failed')),
			result TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	},
}

// runMigrations applies migrations in order, atomically recording each version with its SQL.
func runMigrations(ctx context.Context, db *sql.DB, migrations []migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer tx.Rollback()

	const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`
	if _, err := tx.ExecContext(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	for _, item := range migrations {
		if err := applyMigration(ctx, tx, item); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}

	return nil
}

func applyMigration(ctx context.Context, tx *sql.Tx, item migration) error {
	var alreadyApplied bool
	const checkQuery = `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)`

	if err := tx.QueryRowContext(ctx, checkQuery, item.version).Scan(&alreadyApplied); err != nil {
		return fmt.Errorf("check migration %d: %w", item.version, err)
	}
	if alreadyApplied {
		return nil
	}

	if _, err := tx.ExecContext(ctx, item.sql); err != nil {
		return fmt.Errorf("apply migration %d: %w", item.version, err)
	}

	const insertQuery = `INSERT INTO schema_migrations (version) VALUES (?)`
	if _, err := tx.ExecContext(ctx, insertQuery, item.version); err != nil {
		return fmt.Errorf("record migration %d: %w", item.version, err)
	}

	return nil
}
