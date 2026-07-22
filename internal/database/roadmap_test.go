package database

import (
	"testing"
)

// seedRoadmapFeedback inserts a feedback with the given roadmap fields and
// returns its generated id. project_id is always 'p' so callers can use
// GetPublicRoadmap('p', ...) without tripping the global-project subquery.
func seedRoadmapFeedback(t *testing.T, db *Database, title string, public int, status string, order int) int64 {
	t.Helper()
	if _, err := db.db.Exec(
		`INSERT INTO feedbacks (project_id, title, public_on_roadmap, roadmap_status, roadmap_order) VALUES (?, ?, ?, ?, ?)`,
		"p", title, public, status, order); err != nil {
		t.Fatalf("seed feedback failed: %v", err)
	}
	var id int64
	if err := db.db.QueryRow(`SELECT id FROM feedbacks WHERE title = ?`, title).Scan(&id); err != nil {
		t.Fatalf("get seeded id failed: %v", err)
	}
	return id
}

func TestRoadmapMentionCount(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatal(err)
	}
	cid := seedRoadmapFeedback(t, db, "canonical", 1, "planning", 0)
	// two duplicates pointing at the canonical
	if _, err := db.db.Exec(`INSERT INTO feedbacks (project_id, title, is_duplicate, duplicate_of) VALUES ('p','dup1',1,?)`, cid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT INTO feedbacks (project_id, title, is_duplicate, duplicate_of) VALUES ('p','dup2',1,?)`, cid); err != nil {
		t.Fatal(err)
	}

	pub, err := db.GetPublicRoadmap("p", "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != 1 {
		t.Fatalf("public items = %d, want 1", len(pub))
	}
	if pub[0].MentionCount != 2 {
		t.Fatalf("public mention_count = %d, want 2", pub[0].MentionCount)
	}
	if pub[0].RoadmapStatus != "planning" {
		t.Fatalf("status = %q, want planning", pub[0].RoadmapStatus)
	}

	admin, _, err := db.ListRoadmapForAdmin(50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(admin) != 1 {
		t.Fatalf("admin items = %d, want 1", len(admin))
	}
	if admin[0].MentionCount != 2 {
		t.Fatalf("admin mention_count = %d, want 2", admin[0].MentionCount)
	}
}

func TestRoadmapOrdering(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatal(err)
	}
	seedRoadmapFeedback(t, db, "low", 1, "planning", 0)
	seedRoadmapFeedback(t, db, "high", 1, "planning", 5)

	items, err := db.GetPublicRoadmap("p", "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].Title != "high" {
		t.Fatalf("first item = %q, want high (order should win)", items[0].Title)
	}
	if items[1].Title != "low" {
		t.Fatalf("second item = %q, want low", items[1].Title)
	}
}

func TestRoadmapMeta(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatal(err)
	}
	id := seedRoadmapFeedback(t, db, "meta", 1, "in_progress", 0)
	if err := db.SetRoadmapMeta(id, 3, 1700000000, "张三", "v1.2"); err != nil {
		t.Fatal(err)
	}
	items, err := db.GetPublicRoadmap("p", "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	it := items[0]
	if it.Owner != "张三" || it.Release != "v1.2" || it.TargetDate != 1700000000 || it.RoadmapOrder != 3 {
		t.Fatalf("meta not reflected: %+v", it)
	}

	admin, _, err := db.ListRoadmapForAdmin(50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if admin[0].Owner != "张三" || admin[0].Release != "v1.2" || admin[0].RoadmapOrder != 3 {
		t.Fatalf("admin meta not reflected: %+v", admin[0])
	}
}

func TestRoadmapConfig(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatal(err)
	}
	// No keys seeded in test DB -> safe defaults
	rc := db.GetRoadmapConfig()
	if !rc.AutoBoard {
		t.Fatal("AutoBoard should default true")
	}
	if rc.DefaultPublic {
		t.Fatal("DefaultPublic should default false")
	}
	if rc.DefaultStatus != "planning" {
		t.Fatalf("DefaultStatus = %q, want planning", rc.DefaultStatus)
	}
	if !rc.AutoPromote {
		t.Fatal("AutoPromote should default true")
	}
	if rc.AutoPromoteStatus != "released" {
		t.Fatalf("AutoPromoteStatus = %q, want released", rc.AutoPromoteStatus)
	}

	// Override via config table
	if err := db.SetConfig("roadmap_auto_board", "false", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetConfig("roadmap_default_public", "true", ""); err != nil {
		t.Fatal(err)
	}
	rc2 := db.GetRoadmapConfig()
	if rc2.AutoBoard {
		t.Fatal("AutoBoard should be false after override")
	}
	if !rc2.DefaultPublic {
		t.Fatal("DefaultPublic should be true after override")
	}
}

func TestSetRoadmapState(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatal(err)
	}
	id := seedRoadmapFeedback(t, db, "state", 0, "", 0)
	// Sets status + public
	if err := db.SetRoadmap(id, true, "released"); err != nil {
		t.Fatal(err)
	}
	status, pub, err := db.GetRoadmapState(id)
	if err != nil {
		t.Fatal(err)
	}
	if status != "released" || !pub {
		t.Fatalf("state = (%q,%v), want (released,true)", status, pub)
	}
	// Status-only update keeps public
	if err := db.SetRoadmap(id, pub, "planning"); err != nil {
		t.Fatal(err)
	}
	status, pub, err = db.GetRoadmapState(id)
	if err != nil {
		t.Fatal(err)
	}
	if status != "planning" || !pub {
		t.Fatalf("state = (%q,%v), want (planning,true)", status, pub)
	}
}
