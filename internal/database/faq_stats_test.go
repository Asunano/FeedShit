package database

import (
	"testing"
)

// TestFAQViewCountAndStats covers the C2 FAQ usage-stats building blocks:
// GetFAQByID, IncrementFAQViewCount, CountFAQVotes, and that a feedback vote
// sharing the same numeric id does NOT pollute the FAQ vote count.
func TestFAQViewCountAndStats(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	id, err := db.CreateFAQ("proj-x", "Q1", "A1", 0, true)
	if err != nil {
		t.Fatalf("CreateFAQ: %v", err)
	}

	got, err := db.GetFAQByID(id)
	if err != nil {
		t.Fatalf("GetFAQByID: %v", err)
	}
	if got == nil || got.ID != id {
		t.Fatalf("GetFAQByID mismatch: %+v", got)
	}
	if got.ViewCount != 0 {
		t.Fatalf("initial view_count expected 0, got %d", got.ViewCount)
	}

	n, err := db.IncrementFAQViewCount(id)
	if err != nil {
		t.Fatalf("IncrementFAQViewCount #1: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 view, got %d", n)
	}
	n, err = db.IncrementFAQViewCount(id)
	if err != nil {
		t.Fatalf("IncrementFAQViewCount #2: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 views, got %d", n)
	}

	if c, _ := db.CountFAQVotes(id); c != 0 {
		t.Fatalf("expected 0 faq votes, got %d", c)
	}
	if _, err := db.InsertVote(id, "voter1", "useful", "faq"); err != nil {
		t.Fatalf("insert faq vote 1: %v", err)
	}
	if _, err := db.InsertVote(id, "voter2", "useful", "faq"); err != nil {
		t.Fatalf("insert faq vote 2: %v", err)
	}
	if already, _ := db.InsertVote(id, "voter1", "useful", "faq"); !already {
		t.Fatal("duplicate faq vote should be reported as already-voted")
	}
	if c, _ := db.CountFAQVotes(id); c != 2 {
		t.Fatalf("expected 2 faq votes, got %d", c)
	}

	// A feedback vote on the SAME numeric id must not leak into the FAQ count.
	if _, err := db.InsertVote(id, "voter1", "useful", "feedback"); err != nil {
		t.Fatalf("insert feedback vote: %v", err)
	}
	if c, _ := db.CountFAQVotes(id); c != 2 {
		t.Fatalf("feedback vote polluted faq count: got %d", c)
	}

	if _, err := db.GetFAQByID(999999); err == nil {
		t.Fatal("expected error for missing faq id")
	}
}
