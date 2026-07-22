package database

import (
	"testing"
)

// TestVoteTargetSeparation verifies that feedback_votes rows keyed by
// (feedback_id, voter_key, vote_type, target_type) keep FAQ votes and feedback
// votes independent even when they share the same numeric id.
func TestVoteTargetSeparation(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	const sameID int64 = 42

	if _, err := db.InsertVote(sameID, "u1", "useful", "feedback"); err != nil {
		t.Fatalf("feedback vote u1: %v", err)
	}
	if _, err := db.InsertVote(sameID, "u2", "useful", "feedback"); err != nil {
		t.Fatalf("feedback vote u2: %v", err)
	}
	if _, err := db.InsertVote(sameID, "u1", "useful", "faq"); err != nil {
		t.Fatalf("faq vote u1: %v", err)
	}

	fbUseful, _ := db.CountVotesByType(sameID, "useful", "feedback")
	if fbUseful != 2 {
		t.Fatalf("feedback useful expected 2, got %d", fbUseful)
	}
	faqUseful, _ := db.CountVotesByType(sameID, "useful", "faq")
	if faqUseful != 1 {
		t.Fatalf("faq useful expected 1, got %d", faqUseful)
	}

	// VoteCountMap (used by feedback listings) must count feedback only.
	vmap, _ := db.VoteCountMap([]int64{sameID})
	if vmap[sameID] != 2 {
		t.Fatalf("VoteCountMap expected 2 (feedback only), got %d", vmap[sameID])
	}

	if c, _ := db.CountVotes(sameID); c != 2 {
		t.Fatalf("CountVotes expected 2, got %d", c)
	}
}

// TestVoteTargetTypeDefault confirms an empty target_type defaults to
// "feedback", so legacy callers keep writing feedback votes.
func TestVoteTargetTypeDefault(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	if _, err := db.InsertVote(7, "v", "useful", ""); err != nil {
		t.Fatalf("insert default target: %v", err)
	}
	if c, _ := db.CountVotesByType(7, "useful", ""); c != 1 {
		t.Fatalf("empty target should count as feedback, got %d", c)
	}
	if c, _ := db.CountVotesByType(7, "useful", "faq"); c != 0 {
		t.Fatalf("faq target should be 0, got %d", c)
	}
}
