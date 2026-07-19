package main

// Independent QA verification tests (Phase 0+1) — authored by QA. Proves the
// backup-pruning scheduler wrapper: retention 0 is a no-op (skip), retention 30
// drops old backups while keeping recent ones.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"feedshit/internal/database"
)

func TestQAVerifyPruneBackupsZeroSkips(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.bak")
	if err := os.WriteFile(old, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	ot := time.Now().AddDate(0, 0, -40)
	if err := os.Chtimes(old, ot, ot); err != nil {
		t.Fatal(err)
	}
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	// retention 0 -> documented no-op; nothing must be deleted.
	pruneBackups(db, dir, 0)
	if _, err := os.Stat(old); err != nil {
		t.Fatal("daysOld=0 must skip pruning (file should survive)")
	}
}

func TestQAVerifyPruneBackupsRetention(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.bak")
	recent := filepath.Join(dir, "new.bak")
	_ = os.WriteFile(old, []byte("x"), 0644)
	_ = os.WriteFile(recent, []byte("x"), 0644)
	ot := time.Now().AddDate(0, 0, -40)
	_ = os.Chtimes(old, ot, ot)
	db, _ := database.NewTestDatabase()
	pruneBackups(db, dir, 30)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old backup should be pruned at retention 30")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatal("recent backup should be kept")
	}
}
