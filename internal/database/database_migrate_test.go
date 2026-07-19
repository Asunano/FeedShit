package database

import (
	"testing"
)

// errString is a minimal error type for constructing test errors by message.
type errString string

func (e errString) Error() string { return string(e) }

func TestMigrateIsIdempotent(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	// Running migrate again on an already-migrated DB must not error: all
	// ALTER/CREATE statements are guarded (duplicate column / already exists).
	if err := db.migrate(); err != nil {
		t.Fatalf("second migrate() should be idempotent, got: %v", err)
	}
}

func TestIsIgnorableMigrationErr(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"duplicate column: projects.is_archived", true},
		{"table webhook_subscriptions already exists", true},
		{"UNIQUE constraint failed", false},
		{"no such table: foo", false},
	}
	for _, c := range cases {
		if got := isIgnorableMigrationErr(errString(c.msg)); got != c.want {
			t.Fatalf("isIgnorableMigrationErr(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
	if isIgnorableMigrationErr(nil) {
		t.Fatal("nil error should not be ignorable")
	}
}

func TestExecMigrateIgnoresDuplicateColumn(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	// project_id already exists from the initial schema; this must be ignored.
	if err := db.execMigrate(`ALTER TABLE feedbacks ADD COLUMN project_id TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("execMigrate should ignore duplicate column, got: %v", err)
	}
}

func TestExecMigratePropagatesRealErrors(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	// A non-idempotent, genuinely failing statement must propagate.
	if err := db.execMigrate(`SELECT * FROM table_that_does_not_exist`); err == nil {
		t.Fatal("execMigrate should propagate non-ignorable errors")
	}
}

func TestNewTestDatabaseHasSchema(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	// The config table must exist and be queryable after migration.
	rows, err := db.db.Query(`SELECT key FROM config`)
	if err != nil {
		t.Fatalf("config table missing after migration: %v", err)
	}
	rows.Close()
}
