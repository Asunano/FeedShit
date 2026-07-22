package database

// Audit log filter regression tests (DB layer). Covers action (exact),
// user (substring), created_at range, and combined filters.
import (
	"testing"
	"time"
)

func TestListAuditLogsFilters(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	now := time.Now()
	seed := []struct {
		action string
		user   string
		offset int // days ago
	}{
		{"delete_project", "alice", 0},
		{"delete_project", "bob", 1},
		{"backup", "alice", 2},
		{"login", "carol", 5},
		{"export", "alice", 10},
	}
	// Insert with deterministic created_at (InsertAuditLog uses time.Now()).
	for _, s := range seed {
		ts := now.AddDate(0, 0, -s.offset).Unix()
		if _, err := db.db.Exec(
			`INSERT INTO audit_logs (action, detail, user, ip, created_at) VALUES (?, ?, ?, ?, ?)`,
			s.action, "detail-"+s.action, s.user, "127.0.0.1", ts,
		); err != nil {
			t.Fatalf("seed audit %s: %v", s.action, err)
		}
	}

	// No filter: all 5.
	if _, total, err := db.ListAuditLogs("", "", 0, 0, 100, 0); err != nil || total != 5 {
		t.Fatalf("unfiltered: expected 5, got total=%d err=%v", total, err)
	}

	// Action exact.
	if _, total, _ := db.ListAuditLogs("delete_project", "", 0, 0, 100, 0); total != 2 {
		t.Fatalf("action=delete_project: expected 2, got %d", total)
	}

	// User substring.
	if _, total, _ := db.ListAuditLogs("", "ali", 0, 0, 100, 0); total != 3 {
		t.Fatalf("user~'ali': expected 3, got %d", total)
	}

	// Date range: last 3 days.
	from := now.AddDate(0, 0, -3).Unix()
	if _, total, _ := db.ListAuditLogs("", "", from, 0, 100, 0); total != 3 {
		t.Fatalf("from last 3 days: expected 3, got %d", total)
	}

	// Date range: up to 2 days ago (older side). The boundary `to` is computed
	// the same way the handler does (date string → midnight → +24h), so the
	// entry exactly 2 days ago is included.
	to := now.AddDate(0, 0, -3).Add(24 * time.Hour).Unix()
	if _, total, _ := db.ListAuditLogs("", "", 0, to, 100, 0); total != 3 {
		t.Fatalf("to 2 days ago: expected 3, got %d", total)
	}

	// Combined action + user.
	if _, total, _ := db.ListAuditLogs("delete_project", "bob", 0, 0, 100, 0); total != 1 {
		t.Fatalf("delete_project+bob: expected 1, got %d", total)
	}
}
