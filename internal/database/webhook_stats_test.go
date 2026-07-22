package database

import "testing"

// WebhookDeliveryStats aggregates success/failure correctly, including a 0
// status (request never completed) which counts as a failure.
func TestWebhookDeliveryStats(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	// 2xx, 2xx, 5xx, and 0 (connection failure) → 2 success / 2 failed.
	statuses := []int{200, 200, 500, 0}
	for i, s := range statuses {
		if err := db.RecordWebhookDelivery(int64(5), "delivery", "https://example.com/h", "{}", s, "ok", "", int64(1000+i)); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	total, success, failed, rate, err := db.WebhookDeliveryStats(5)
	if err != nil {
		t.Fatalf("WebhookDeliveryStats: %v", err)
	}
	if total != 4 {
		t.Fatalf("total expected 4, got %d", total)
	}
	if success != 2 {
		t.Fatalf("success expected 2, got %d", success)
	}
	if failed != 2 {
		t.Fatalf("failed expected 2, got %d", failed)
	}
	if rate < 49.9 || rate > 50.1 {
		t.Fatalf("rate expected ~50, got %f", rate)
	}

	// Unknown subscription → all zeros, no error.
	t2, s2, f2, r2, err := db.WebhookDeliveryStats(999)
	if err != nil {
		t.Fatalf("stats unknown sub: %v", err)
	}
	if t2 != 0 || s2 != 0 || f2 != 0 || r2 != 0 {
		t.Fatalf("unknown sub should be zeros, got %d/%d/%d/%f", t2, s2, f2, r2)
	}
}
