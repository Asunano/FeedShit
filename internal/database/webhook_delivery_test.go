package database

// Webhook delivery history (DB layer): record + list with ordering/limit.
import "testing"

func TestWebhookDeliveryRecordAndList(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := db.RecordWebhookDelivery(int64(7), "delivery", "https://example.com/h", "{}", 200, "ok", "", int64(1000+i)); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	// Limit 3 → returns 3, most recent first.
	dl, err := db.ListWebhookDeliveries(7, 3)
	if err != nil {
		t.Fatalf("ListWebhookDeliveries: %v", err)
	}
	if len(dl) != 3 {
		t.Fatalf("expected 3 deliveries, got %d", len(dl))
	}
	if dl[0].CreatedAt.Unix() != 1004 {
		t.Fatalf("expected most recent (1004) first, got %d", dl[0].CreatedAt.Unix())
	}
	// Default limit (0) → capped at 20, returns all 5.
	all, err := db.ListWebhookDeliveries(7, 0)
	if err != nil {
		t.Fatalf("ListWebhookDeliveries default: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("expected 5 deliveries with default limit, got %d", len(all))
	}
	// Other subscription id returns nothing.
	other, _ := db.ListWebhookDeliveries(99, 20)
	if len(other) != 0 {
		t.Fatalf("expected 0 deliveries for other sub, got %d", len(other))
	}
}
