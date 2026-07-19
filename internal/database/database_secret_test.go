package database

import (
	"bytes"
	"log"
	"os"
	"testing"

	"feedshit/internal/security"
)

// testKey is the fixed 32-byte master key used across all database tests that
// exercise at-rest encryption (secrets, webhook subscriptions).
var testKey = bytes.Repeat([]byte{0xAB}, 32)

// TestMain initializes the security master key before any database test runs,
// so encryption/decryption helpers behave deterministically.
func TestMain(m *testing.M) {
	if err := security.InitWithKey(testKey); err != nil {
		log.Fatalf("test setup: failed to init security master key: %v", err)
	}
	os.Exit(m.Run())
}

func TestConfigSensitiveValueEncryptedAtRest(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	if err := db.SetConfig("smtp_pass", "super-secret-smtp", "SMTP 密码"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	// Read path returns the cleartext value.
	if got := db.GetConfig("smtp_pass"); got != "super-secret-smtp" {
		t.Fatalf("GetConfig returned %q, want cleartext", got)
	}
	// Stored value MUST be encrypted (never plaintext at rest).
	var raw string
	if err := db.db.QueryRow(`SELECT value FROM config WHERE key = ?`, "smtp_pass").Scan(&raw); err != nil {
		t.Fatalf("raw read failed: %v", err)
	}
	if !security.IsEncrypted(raw) {
		t.Fatalf("sensitive config stored in plaintext at rest: %q", raw)
	}
}

func TestConfigNonSensitiveValueNotEncrypted(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	if err := db.SetConfig("smtp_host", "smtp.example.com", "SMTP 主机"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	if got := db.GetConfig("smtp_host"); got != "smtp.example.com" {
		t.Fatalf("GetConfig returned %q, want %q", got, "smtp.example.com")
	}
	var raw string
	if err := db.db.QueryRow(`SELECT value FROM config WHERE key = ?`, "smtp_host").Scan(&raw); err != nil {
		t.Fatalf("raw read failed: %v", err)
	}
	if security.IsEncrypted(raw) {
		t.Fatalf("non-sensitive config should not be encrypted: %q", raw)
	}
}

func TestWebhookSecretEncryptedRoundTrip(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	id, err := db.CreateWebhookSubscription("acme", "https://hooks.acme.com/x", "wh-secret-123", "*", true)
	if err != nil {
		t.Fatalf("CreateWebhookSubscription failed: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}
	// ListWebhookSubscriptions returns the plaintext secret (masked-secret contract
	// is enforced at the API layer, not here).
	subs, err := db.ListWebhookSubscriptions()
	if err != nil {
		t.Fatalf("ListWebhookSubscriptions failed: %v", err)
	}
	var found *WebhookSubscription
	for i := range subs {
		if subs[i].ID == id {
			found = &subs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created subscription not found in list")
	}
	if found.Secret != "wh-secret-123" {
		t.Fatalf("ListWebhookSubscriptions returned %q, want cleartext secret", found.Secret)
	}
	// Stored secret MUST be encrypted at rest.
	var raw string
	if err := db.db.QueryRow(`SELECT secret FROM webhook_subscriptions WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("raw read failed: %v", err)
	}
	if !security.IsEncrypted(raw) {
		t.Fatalf("webhook secret stored in plaintext at rest: %q", raw)
	}
}

func TestUpdateWebhookSecretPreservesWhenEmpty(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	id, err := db.CreateWebhookSubscription("acme", "https://hooks.acme.com/x", "original-secret", "*", true)
	if err != nil {
		t.Fatalf("CreateWebhookSubscription failed: %v", err)
	}
	// Update with empty secret -> existing secret preserved (and re-encrypted).
	if err := db.UpdateWebhookSubscription(id, "https://hooks.acme.com/y", "", "*", nil); err != nil {
		t.Fatalf("UpdateWebhookSubscription failed: %v", err)
	}
	subs, err := db.ListWebhookSubscriptions()
	if err != nil {
		t.Fatalf("ListWebhookSubscriptions failed: %v", err)
	}
	for _, s := range subs {
		if s.ID == id && s.Secret != "original-secret" {
			t.Fatalf("secret not preserved on empty update: got %q", s.Secret)
		}
	}
	// Update with new secret -> replaced.
	if err := db.UpdateWebhookSubscription(id, "", "new-secret", "", nil); err != nil {
		t.Fatalf("UpdateWebhookSubscription failed: %v", err)
	}
	subs, _ = db.ListWebhookSubscriptions()
	for _, s := range subs {
		if s.ID == id && s.Secret != "new-secret" {
			t.Fatalf("secret not updated: got %q", s.Secret)
		}
	}
}

func TestReEncryptSecretsUpgradesPlaintext(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	// Insert a legacy plaintext smtp_pass directly (bypassing SetConfig encryption).
	if _, err := db.db.Exec(
		`INSERT INTO config (key, value, description) VALUES ('smtp_pass', 'legacy-plaintext', 'SMTP 密码')
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
	); err != nil {
		t.Fatalf("raw insert failed: %v", err)
	}
	// Insert a legacy plaintext webhook secret.
	whID, err := db.CreateWebhookSubscription("acme", "https://hooks.acme.com/x", "legacy-wh", "*", true)
	if err != nil {
		t.Fatalf("CreateWebhookSubscription failed: %v", err)
	}
	// Downgrade the stored webhook secret to plaintext to simulate legacy data.
	if _, err := db.db.Exec(`UPDATE webhook_subscriptions SET secret = ? WHERE id = ?`, "legacy-wh", whID); err != nil {
		t.Fatalf("raw update failed: %v", err)
	}

	if err := db.ReEncryptSecrets(); err != nil {
		t.Fatalf("ReEncryptSecrets failed: %v", err)
	}

	// Both values are now readable as cleartext.
	if got := db.GetConfig("smtp_pass"); got != "legacy-plaintext" {
		t.Fatalf("smtp_pass not readable after re-encrypt: %q", got)
	}
	subs, _ := db.ListWebhookSubscriptions()
	for _, s := range subs {
		if s.ID == whID && s.Secret != "legacy-wh" {
			t.Fatalf("webhook secret not readable after re-encrypt: %q", s.Secret)
		}
	}
	// And both are now encrypted at rest.
	var cfgRaw, whRaw string
	db.db.QueryRow(`SELECT value FROM config WHERE key = 'smtp_pass'`).Scan(&cfgRaw)
	db.db.QueryRow(`SELECT secret FROM webhook_subscriptions WHERE id = ?`, whID).Scan(&whRaw)
	if !security.IsEncrypted(cfgRaw) {
		t.Fatalf("smtp_pass not encrypted after ReEncryptSecrets: %q", cfgRaw)
	}
	if !security.IsEncrypted(whRaw) {
		t.Fatalf("webhook secret not encrypted after ReEncryptSecrets: %q", whRaw)
	}
}

func TestReEncryptSecretsIdempotent(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	if err := db.SetConfig("smtp_pass", "already-encrypted-path", "SMTP 密码"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	// Running twice must not error and must keep the value decryptable.
	if err := db.ReEncryptSecrets(); err != nil {
		t.Fatalf("first ReEncryptSecrets failed: %v", err)
	}
	if err := db.ReEncryptSecrets(); err != nil {
		t.Fatalf("second ReEncryptSecrets failed: %v", err)
	}
	if got := db.GetConfig("smtp_pass"); got != "already-encrypted-path" {
		t.Fatalf("value changed after idempotent re-encrypt: %q", got)
	}
}
