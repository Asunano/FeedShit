package database

// UpdateAdminLastLogin + GetAdminByID reflect the last-login timestamp.
import "testing"

func TestUpdateAdminLastLogin(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	res, err := db.db.Exec(
		`INSERT INTO admins (username, password_hash, role, is_active, last_login_at) VALUES (?, ?, 'editor', 1, 0)`,
		"lluser", "hash")
	if err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	id, _ := res.LastInsertId()
	ts := int64(1234567890)
	if err := db.UpdateAdminLastLogin(id, ts); err != nil {
		t.Fatalf("UpdateAdminLastLogin: %v", err)
	}
	a, err := db.GetAdminByID(id)
	if err != nil {
		t.Fatalf("GetAdminByID: %v", err)
	}
	if a == nil || a.LastLoginAt != ts {
		t.Fatalf("expected last_login_at=%d, got %d", ts, a.LastLoginAt)
	}
}
