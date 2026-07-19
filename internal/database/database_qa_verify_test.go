package database

// Independent QA verification tests (Phase 0+1) — authored by QA to prove
// critical behaviors beyond the engineer's self-tests. These target coverage
// gaps and the highest-risk contract (webhook secret mask).

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Point #1 (highest risk): the mask contract must hold end-to-end. The DB layer
// returns the plaintext (decrypted) secret, and EnqueueWebhook must write that
// plaintext into the outbox so HMAC signing at delivery time uses the real
// secret. If the outbox ever received a masked or ciphertext value, HMAC would
// fail on the receiver side.
func TestQAVerifyWebhookOutboxCarriesPlaintextSecret(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	// Secret with special characters + unicode + whitespace to stress encryption.
	const secret = `sec#$%^&*()+|"'` + "中文字符" + "\n\t"
	id, err := db.CreateWebhookSubscription("acme", "https://hooks.acme.com/x", secret, "*", true)
	if err != nil {
		t.Fatalf("CreateWebhookSubscription failed: %v", err)
	}
	if err := db.EnqueueWebhook("new_feedback", `{"event":1}`, "acme"); err != nil {
		t.Fatalf("EnqueueWebhook failed: %v", err)
	}
	var got string
	if err := db.db.QueryRow(`SELECT secret FROM webhook_outbox WHERE subscription_id = ?`, id).Scan(&got); err != nil {
		t.Fatalf("outbox read failed: %v", err)
	}
	if got != secret {
		t.Fatalf("outbox secret = %q, want %q (a masked/ciphertext value would break HMAC signing)", got, secret)
	}
	// The DB list layer also returns the plaintext pre-mask.
	subs, _ := db.ListWebhookSubscriptions()
	for _, s := range subs {
		if s.ID == id && s.Secret != secret {
			t.Fatalf("ListWebhookSubscriptions returned %q, want plaintext %q", s.Secret, secret)
		}
	}
}

// Point #8: buildAccessPlanWhere SQL WHERE correctness for wildcard and
// category-scoped RBAC plans.
func TestQAVerifyBuildAccessPlanWhere(t *testing.T) {
	// wildcard (nil AllowedCategories) -> simple project_id filter
	where, args := buildAccessPlanWhere([]ProjectAccess{{Slug: "p1"}})
	if where != " WHERE project_id = ?" || len(args) != 1 || args[0] != "p1" {
		t.Fatalf("wildcard plan wrong: %q %v", where, args)
	}
	// category-scoped -> (category IN (...) AND project_id = ?)
	where, args = buildAccessPlanWhere([]ProjectAccess{{Slug: "p1", AllowedCategories: []string{"bug", "feature"}}})
	if where != " WHERE (category IN (?,?) AND project_id = ?)" || len(args) != 3 {
		t.Fatalf("category plan wrong: %q %v", where, args)
	}
	if args[2] != "p1" {
		t.Fatalf("category plan project arg wrong: %v", args)
	}
	// empty plan -> no access
	where, _ = buildAccessPlanWhere(nil)
	if where != " WHERE 1=0" {
		t.Fatalf("empty plan should be no-access: %q", where)
	}
	// multiple projects are OR'd together
	where, args = buildAccessPlanWhere([]ProjectAccess{
		{Slug: "p1"},
		{Slug: "p2", AllowedCategories: []string{"bug"}},
	})
	want := " WHERE (project_id = ? OR (category IN (?) AND project_id = ?))"
	if where != want || len(args) != 3 {
		t.Fatalf("multi plan wrong: %q %v (want %q)", where, args, want)
	}
}

// Point #8: GetEffectiveRole category vs wildcard grant isolation.
func TestQAVerifyGetEffectiveRole(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	adminID, err := db.CreateAdmin("qa", "h", "editor")
	if err != nil {
		t.Fatalf("CreateAdmin failed: %v", err)
	}
	db.db.Exec(`INSERT INTO member_grants (admin_id, project_slug, category_key, role) VALUES (?, ?, '*', 'viewer')`, adminID, "proj-a")
	if r := db.GetEffectiveRole(adminID, "proj-a", "bug"); r != "viewer" {
		t.Fatalf("expected viewer via wildcard, got %q", r)
	}
	db.db.Exec(`INSERT INTO member_grants (admin_id, project_slug, category_key, role) VALUES (?, ?, 'bug', 'editor')`, adminID, "proj-a")
	if r := db.GetEffectiveRole(adminID, "proj-a", "bug"); r != "editor" {
		t.Fatalf("expected editor for bug category, got %q", r)
	}
	if r := db.GetEffectiveRole(adminID, "proj-b", "bug"); r != "" {
		t.Fatalf("expected no role for ungranted project, got %q", r)
	}
}

// Point #6: retention-based pruning at the DB layer (30 days keeps recent, drops old).
func TestQAVerifyPruneOldBackupsRetention(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	dir := t.TempDir()
	old := filepath.Join(dir, "old.bak")
	now := filepath.Join(dir, "new.bak")
	if err := os.WriteFile(old, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(now, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	ot := time.Now().AddDate(0, 0, -40)
	if err := os.Chtimes(old, ot, ot); err != nil {
		t.Fatal(err)
	}
	n, err := db.PruneOldBackups(dir, 30)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 pruned, got %d", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old backup should be pruned at retention 30")
	}
	if _, err := os.Stat(now); err != nil {
		t.Fatal("new backup should be kept")
	}
}
