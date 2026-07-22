package database

import "testing"

// TestTokenCallRecordingAndStats verifies that RecordTokenCall persists events
// and TokenCallStats returns exactly 24 hourly buckets with correct ok/fail
// splits, and zero-fills for tokens with no activity.
func TestTokenCallRecordingAndStats(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase: %v", err)
	}

	id, err := db.CreateAPIToken("fs_calltest", "calltest", "", 0, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// 3 successes + 2 rate-limit rejections.
	for i := 0; i < 3; i++ {
		if err := db.RecordTokenCall(id, 200); err != nil {
			t.Fatalf("RecordTokenCall 200: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := db.RecordTokenCall(id, 429); err != nil {
			t.Fatalf("RecordTokenCall 429: %v", err)
		}
	}

	buckets, err := db.TokenCallStats(id)
	if err != nil {
		t.Fatalf("TokenCallStats: %v", err)
	}
	if len(buckets) != 24 {
		t.Fatalf("expected 24 buckets, got %d", len(buckets))
	}

	totalOK, totalFail := 0, 0
	for _, b := range buckets {
		if b.OK+b.Fail != b.Count {
			t.Fatalf("bucket count mismatch: ok=%d fail=%d count=%d", b.OK, b.Fail, b.Count)
		}
		totalOK += b.OK
		totalFail += b.Fail
	}
	if totalOK != 3 {
		t.Fatalf("expected 3 ok calls, got %d", totalOK)
	}
	if totalFail != 2 {
		t.Fatalf("expected 2 fail calls, got %d", totalFail)
	}

	// A token with no activity must produce 24 zero-filled buckets.
	empty, err := db.TokenCallStats(999999)
	if err != nil {
		t.Fatalf("TokenCallStats(empty): %v", err)
	}
	for _, b := range empty {
		if b.Count != 0 || b.OK != 0 || b.Fail != 0 {
			t.Fatalf("unexpected non-zero bucket for missing token: %+v", b)
		}
	}
}
