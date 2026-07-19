package database

// M9 FAQ self-service regression tests (DB layer).
// Covers acceptance items: cross-project isolation (1), lifecycle / inactive
// filtering + hard delete (4), and migration idempotency (6). Boundary codes
// (400/404/409) and the public response shape (2) are exercised at the handler
// layer in internal/app/faq_test.go.

import (
	"database/sql"
	"testing"
)

// faqColumns returns the column names of the faqs table via PRAGMA table_info.
func faqColumns(d *Database) ([]string, error) {
	rows, err := d.db.Query(`PRAGMA table_info(faqs)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, nil
}

// Item 1: FAQs of project A are never returned when searching project B.
func TestFAQCrossProjectIsolation(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	if _, err := db.CreateFAQ("proj-a", "A question one", "a1", 0, true); err != nil {
		t.Fatalf("seed proj-a/1: %v", err)
	}
	if _, err := db.CreateFAQ("proj-a", "A question two", "a2", 0, true); err != nil {
		t.Fatalf("seed proj-a/2: %v", err)
	}
	if _, err := db.CreateFAQ("proj-b", "B question one", "b1", 0, true); err != nil {
		t.Fatalf("seed proj-b/1: %v", err)
	}
	if _, err := db.CreateFAQ("proj-b", "B question two", "b2", 0, true); err != nil {
		t.Fatalf("seed proj-b/2: %v", err)
	}

	resA, err := db.SearchFAQs("proj-a", "%", 50)
	if err != nil {
		t.Fatalf("SearchFAQs proj-a: %v", err)
	}
	if len(resA) != 2 {
		t.Fatalf("proj-a search expected 2 rows, got %d", len(resA))
	}
	for _, f := range resA {
		if f.ProjectSlug != "proj-a" {
			t.Fatalf("cross-project leak: row slug=%q question=%q", f.ProjectSlug, f.Question)
		}
	}

	resB, err := db.SearchFAQs("proj-b", "%", 50)
	if err != nil {
		t.Fatalf("SearchFAQs proj-b: %v", err)
	}
	if len(resB) != 2 {
		t.Fatalf("proj-b search expected 2 rows, got %d", len(resB))
	}
	for _, f := range resB {
		if f.ProjectSlug != "proj-b" {
			t.Fatalf("cross-project leak: row slug=%q question=%q", f.ProjectSlug, f.Question)
		}
	}

	listA, err := db.ListFAQs("proj-a")
	if err != nil {
		t.Fatalf("ListFAQs proj-a: %v", err)
	}
	if len(listA) != 2 {
		t.Fatalf("proj-a list expected 2 rows, got %d", len(listA))
	}
}

// Item 4: is_active=0 must be excluded from the public search but visible in
// the admin list with IsActive=false.
func TestFAQInactiveExcludedFromPublic(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	if _, err := db.CreateFAQ("proj-a", "Active Q", "ans", 0, true); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	idInactive, err := db.CreateFAQ("proj-a", "Inactive Q", "ans", 0, false)
	if err != nil {
		t.Fatalf("seed inactive: %v", err)
	}

	res, err := db.SearchFAQs("proj-a", "%", 50)
	if err != nil {
		t.Fatalf("SearchFAQs: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("public search expected exactly 1 active row, got %d", len(res))
	}
	if res[0].Question != "Active Q" {
		t.Fatalf("unexpected public row: %q", res[0].Question)
	}

	list, err := db.ListFAQs("proj-a")
	if err != nil {
		t.Fatalf("ListFAQs: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("admin list expected 2 rows (incl inactive), got %d", len(list))
	}
	found := false
	for _, f := range list {
		if f.ID == idInactive {
			found = true
			if f.IsActive {
				t.Fatalf("inactive FAQ must report IsActive=false in admin list")
			}
		}
	}
	if !found {
		t.Fatalf("inactive FAQ missing from admin list")
	}
}

// Item 3 (root cause of 409) + cross-project allowance: duplicate
// (project_slug, question) is rejected, but the same question in another
// project is allowed.
func TestFAQDuplicateQuestionConflict(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	if _, err := db.CreateFAQ("proj-a", "Dup Q", "ans", 0, true); err != nil {
		t.Fatalf("seed first: %v", err)
	}
	if _, err := db.CreateFAQ("proj-a", "Dup Q", "ans2", 0, true); err == nil {
		t.Fatal("expected UNIQUE conflict for duplicate question in same project")
	}
	if _, err := db.CreateFAQ("proj-b", "Dup Q", "ans", 0, true); err != nil {
		t.Fatalf("cross-project duplicate should be allowed: %v", err)
	}
}

// Item 4 + Item 1 constraint: hard delete removes the row; a second delete and
// a cross-project delete are both no-ops (sql.ErrNoRows -> 404).
func TestFAQHardDelete(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	id, err := db.CreateFAQ("proj-a", "Del Q", "ans", 0, true)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.DeleteFAQ(id, "proj-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, err := db.GetFAQByQuestion("proj-a", "Del Q"); err != sql.ErrNoRows {
		t.Fatalf("after delete expected ErrNoRows, got err=%v faq=%v", err, got)
	}
	if err := db.DeleteFAQ(id, "proj-a"); err != sql.ErrNoRows {
		t.Fatalf("repeat delete should be ErrNoRows, got %v", err)
	}

	idB, err := db.CreateFAQ("proj-b", "Del Q", "ans", 0, true)
	if err != nil {
		t.Fatalf("seed proj-b: %v", err)
	}
	if err := db.DeleteFAQ(idB, "proj-a"); err != sql.ErrNoRows {
		t.Fatalf("cross-project delete must be no-op (ErrNoRows), got %v", err)
	}
	if _, err := db.GetFAQByQuestion("proj-b", "Del Q"); err != nil {
		t.Fatalf("proj-b FAQ should survive cross-project delete, got %v", err)
	}
}

// Signature deviation (intended): UpdateFAQ only touches rows owned by
// projectSlug; updating as a different project yields sql.ErrNoRows and leaves
// the original untouched.
func TestFAQUpdateProjectConstraint(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	id, err := db.CreateFAQ("proj-a", "Upd Q", "ans", 0, true)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.UpdateFAQ(id, "proj-b", "Upd Q changed", "ans2", 0, true); err != sql.ErrNoRows {
		t.Fatalf("cross-project update must be ErrNoRows, got %v", err)
	}
	f, err := db.GetFAQByQuestion("proj-a", "Upd Q")
	if err != nil || f == nil {
		t.Fatalf("original FAQ should be unchanged, err=%v", err)
	}
	if _, err := db.GetFAQByQuestion("proj-b", "Upd Q changed"); err != sql.ErrNoRows {
		t.Fatalf("proj-b must not receive the updated row, got %v", err)
	}
}

// Item 4 (search quality): LIKE matching is case-insensitive (LOWER).
func TestFAQLikeCaseInsensitive(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	if _, err := db.CreateFAQ("proj-a", "HOW TO RESET PASSWORD?", "Click settings", 0, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := db.SearchFAQs("proj-a", "%reset password%", 50)
	if err != nil {
		t.Fatalf("lowercase search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("case-insensitive LIKE (lower) expected 1, got %d", len(res))
	}
	res2, err := db.SearchFAQs("proj-a", "%RESET PASSWORD%", 50)
	if err != nil {
		t.Fatalf("uppercase search: %v", err)
	}
	if len(res2) != 1 {
		t.Fatalf("case-insensitive LIKE (upper) expected 1, got %d", len(res2))
	}
}

// Item 4 (search quality): a question hit ranks before an answer-only hit.
func TestFAQSearchOrdering(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	if _, err := db.CreateFAQ("proj-a", "Refund policy", "see answer", 0, true); err != nil {
		t.Fatalf("seed question-hit: %v", err)
	}
	if _, err := db.CreateFAQ("proj-a", "Other topic", "refund policy explained here", 0, true); err != nil {
		t.Fatalf("seed answer-hit: %v", err)
	}
	res, err := db.SearchFAQs("proj-a", "%refund policy%", 50)
	if err != nil {
		t.Fatalf("SearchFAQs: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(res))
	}
	if res[0].Question != "Refund policy" {
		t.Fatalf("question-hit should rank first, got %q", res[0].Question)
	}
}

// Item 6: migration is idempotent (fresh instance + repeat migrate) and the
// faqs table carries all expected columns.
func TestFAQMigrationIdempotent(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	if err := db.migrate(); err != nil {
		t.Fatalf("second migrate() should be idempotent, got: %v", err)
	}
	cols, err := faqColumns(db)
	if err != nil {
		t.Fatalf("faqColumns: %v", err)
	}
	need := map[string]bool{
		"id": true, "project_slug": true, "question": true, "answer": true,
		"embedding": true, "is_active": true, "sort_order": true,
		"created_at": true, "updated_at": true,
	}
	for _, c := range cols {
		delete(need, c)
	}
	if len(need) != 0 {
		t.Fatalf("faqs table missing columns: %v", need)
	}
	id, err := db.CreateFAQ("mig", "Mig Q", "ans", 1, true)
	if err != nil {
		t.Fatalf("insert after migrate: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}
}
