package database

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workspace #1.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	first, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.Conn(ctx)
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	var enabled int
	if err := second.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Errorf("foreign keys on second connection = %d, want 1", enabled)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = db.ExecContext(ctx, `CREATE TABLE parent (id INTEGER PRIMARY KEY); CREATE TABLE child (parent_id INTEGER REFERENCES parent(id))`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO child VALUES (1)`); err == nil {
		t.Fatal("foreign key violation succeeded")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM child`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("child rows = %d, want 0", count)
	}
}

func TestOpenErrors(t *testing.T) {
	ctx := context.Background()
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing", "db.sqlite")} {
		if db, err := Open(ctx, path); err == nil {
			db.Close()
			t.Errorf("Open(%q) succeeded", path)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if db, err := Open(ctx, filepath.Join(t.TempDir(), "db.sqlite")); err == nil {
		db.Close()
		t.Fatal("Open with canceled context succeeded")
	}
}

func TestMigrations(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	first := []migration{{100, `CREATE TABLE example (id INTEGER PRIMARY KEY)`}}
	for i := 0; i < 2; i++ {
		if err := migrate(ctx, db, first); err != nil {
			t.Fatal(err)
		}
	}
	bad := []migration{{101, `CREATE TABLE rolled_back (id INTEGER)`}, {102, `INVALID SQL`}}
	if err := migrate(ctx, db, bad); err == nil || !strings.Contains(err.Error(), "migration 102") {
		t.Fatalf("expected migration 102 error, got %v", err)
	}
	var versions, tables int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = 'rolled_back'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if versions != 2 || tables != 0 {
		t.Errorf("versions = %d, rolled back tables = %d; want 2, 0", versions, tables)
	}
}
