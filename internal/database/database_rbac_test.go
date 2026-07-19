package database

import (
	"testing"
)

func TestAdminCRUDLifecycle(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	id, err := db.CreateAdmin("alice", "hash", "admin")
	if err != nil {
		t.Fatalf("CreateAdmin failed: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	// Username lookup finds the admin.
	a, err := db.GetAdminByUsername("alice")
	if err != nil {
		t.Fatalf("GetAdminByUsername failed: %v", err)
	}
	if a == nil || a.Role != "admin" || a.Username != "alice" {
		t.Fatalf("lookup mismatch: %+v", a)
	}

	// Unknown user returns nil without error.
	none, err := db.GetAdminByUsername("nobody")
	if err != nil {
		t.Fatalf("GetAdminByUsername(none) failed: %v", err)
	}
	if none != nil {
		t.Fatalf("expected nil for unknown user, got %+v", none)
	}

	// Update role.
	if err := db.UpdateAdmin(id, "editor", true, ""); err != nil {
		t.Fatalf("UpdateAdmin failed: %v", err)
	}
	byID, err := db.GetAdminByID(id)
	if err != nil {
		t.Fatalf("GetAdminByID failed: %v", err)
	}
	if byID == nil || byID.Role != "editor" {
		t.Fatalf("role not updated: %+v", byID)
	}

	// Delete.
	if err := db.DeleteAdmin(id); err != nil {
		t.Fatalf("DeleteAdmin failed: %v", err)
	}
	byID, _ = db.GetAdminByID(id)
	if byID != nil {
		t.Fatalf("admin should be deleted, got %+v", byID)
	}
}

func TestListAndCountAdmins(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	if _, err := db.CreateAdmin("u1", "h", "admin"); err != nil {
		t.Fatalf("CreateAdmin failed: %v", err)
	}
	if _, err := db.CreateAdmin("u2", "h", "editor"); err != nil {
		t.Fatalf("CreateAdmin failed: %v", err)
	}
	list, err := db.ListAdmins()
	if err != nil {
		t.Fatalf("ListAdmins failed: %v", err)
	}
	if len(list) < 2 {
		t.Fatalf("expected at least 2 admins, got %d", len(list))
	}
	count, err := db.CountAdmins()
	if err != nil {
		t.Fatalf("CountAdmins failed: %v", err)
	}
	if count < 2 {
		t.Fatalf("expected count >= 2, got %d", count)
	}
}

func TestGetAdminProjectSlugsAdminUnrestricted(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	// Admin role always sees everything (nil = unrestricted).
	slugs, err := db.GetAdminProjectSlugs(999, "admin")
	if err != nil {
		t.Fatalf("GetAdminProjectSlugs failed: %v", err)
	}
	if slugs != nil {
		t.Fatalf("admin should be unrestricted (nil), got %v", slugs)
	}
}

func TestGetAdminProjectSlugsFromGrants(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	// Create an admin and grant access to two projects.
	adminID, err := db.CreateAdmin("bob", "h", "editor")
	if err != nil {
		t.Fatalf("CreateAdmin failed: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO member_grants (admin_id, project_slug, category_key, role) VALUES (?, ?, '*', 'viewer')`,
		adminID, "proj-a"); err != nil {
		t.Fatalf("insert grant failed: %v", err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO member_grants (admin_id, project_slug, category_key, role) VALUES (?, ?, '*', 'viewer')`,
		adminID, "proj-b"); err != nil {
		t.Fatalf("insert grant failed: %v", err)
	}

	slugs, err := db.GetAdminProjectSlugs(adminID, "editor")
	if err != nil {
		t.Fatalf("GetAdminProjectSlugs failed: %v", err)
	}
	if len(slugs) != 2 {
		t.Fatalf("expected 2 granted slugs, got %v", slugs)
	}
	seen := map[string]bool{}
	for _, s := range slugs {
		seen[s] = true
	}
	if !seen["proj-a"] || !seen["proj-b"] {
		t.Fatalf("granted slugs missing: %v", slugs)
	}

	// A different admin with no grants gets no access.
	otherID, _ := db.CreateAdmin("carol", "h", "viewer")
	empty, err := db.GetAdminProjectSlugs(otherID, "viewer")
	if err != nil {
		t.Fatalf("GetAdminProjectSlugs failed: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no access for ungranted admin, got %v", empty)
	}
}
