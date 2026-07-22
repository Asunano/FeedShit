package database

import (
	"database/sql"
	"strings"
	"time"

	"feedshit/internal/security"
)

func (d *Database) CreateWebhookSubscription(projectSlug, url, secret, events string, isActive bool) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	active := 0
	if isActive {
		active = 1
	}
	// Secrets are encrypted at rest; a non-empty secret is stored as a
	// ciphertext token (aes-gcm:...). Empty secrets stay empty.
	storedSecret := secret
	if secret != "" {
		enc, err := security.EncryptWithMaster(secret)
		if err != nil {
			return 0, err
		}
		storedSecret = enc
	}
	res, err := d.db.Exec(`INSERT INTO webhook_subscriptions (project_slug, url, secret, events, is_active) VALUES (?, ?, ?, ?, ?)`,
		projectSlug, url, storedSecret, events, active)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *Database) ListWebhookSubscriptions() ([]WebhookSubscription, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT id, project_slug, url, secret, events, is_active, created_at FROM webhook_subscriptions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []WebhookSubscription
	for rows.Next() {
		var s WebhookSubscription
		var isActive, createdAt int64
		if err := rows.Scan(&s.ID, &s.ProjectSlug, &s.URL, &s.Secret, &s.Events, &isActive, &createdAt); err != nil {
			return nil, err
		}
		// Return plaintext (decrypted) secret to callers. This keeps the
		// masked-secret contract: the DB layer returns secrets in clear so the
		// API handler can decide whether/how to mask them. A value that is not
		// an encrypted token (e.g. a legacy plaintext secret) is left untouched.
		if s.Secret != "" && security.IsEncrypted(s.Secret) {
			if plain, derr := security.DecryptWithMaster(s.Secret); derr == nil {
				s.Secret = plain
			}
		}
		s.IsActive = isActive == 1
		s.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, s)
	}
	return list, nil
}

func (d *Database) UpdateWebhookSubscription(id int64, url, secret, events string, isActive *bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	setClauses := []string{}
	args := []interface{}{}
	if url != "" {
		setClauses = append(setClauses, "url = ?")
		args = append(args, url)
	}
	if secret != "" {
		// New secret supplied: encrypt before persisting.
		enc, err := security.EncryptWithMaster(secret)
		if err != nil {
			return err
		}
		setClauses = append(setClauses, "secret = ?")
		args = append(args, enc)
	} else {
		// Empty secret means "keep existing": leave the secret column untouched
		// so the stored value is preserved exactly. Master-key consistency for
		// already-stored secrets is handled at startup by ReEncryptSecrets, so
		// re-fetching and re-encrypting here is unnecessary and risks corrupting
		// the secret if decryption under a rotated key were to fail.
	}
	if events != "" {
		setClauses = append(setClauses, "events = ?")
		args = append(args, events)
	}
	if isActive != nil {
		v := 0
		if *isActive {
			v = 1
		}
		setClauses = append(setClauses, "is_active = ?")
		args = append(args, v)
	}
	if len(setClauses) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := d.db.Exec(`UPDATE webhook_subscriptions SET `+strings.Join(setClauses, ", ")+` WHERE id = ?`, args...)
	return err
}

// ReEncryptSecrets scans sensitive config values and webhook subscription
// secrets, and re-encrypts any value that is non-empty but not yet stored as a
// ciphertext token. This upgrades legacy plaintext secrets to encryption at
// rest after a master key is first configured, and keeps already-encrypted
// values untouched (idempotent).
func (d *Database) ReEncryptSecrets() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Sensitive config rows.
	for key := range sensitiveConfigKeys {
		var raw string
		err := d.db.QueryRow(`SELECT value FROM config WHERE key = ?`, key).Scan(&raw)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return err
		}
		if raw == "" || security.IsEncrypted(raw) {
			continue
		}
		enc, eerr := security.EncryptWithMaster(raw)
		if eerr != nil {
			return eerr
		}
		if _, uerr := d.db.Exec(`UPDATE config SET value = ? WHERE key = ?`, enc, key); uerr != nil {
			return uerr
		}
	}

	// Webhook subscription secrets.
	rows, err := d.db.Query(`SELECT id, secret FROM webhook_subscriptions`)
	if err != nil {
		return err
	}
	pairs := make([]struct {
		id     int64
		secret string
	}, 0)
	for rows.Next() {
		var p struct {
			id     int64
			secret string
		}
		if rerr := rows.Scan(&p.id, &p.secret); rerr != nil {
			rows.Close()
			return rerr
		}
		pairs = append(pairs, p)
	}
	rows.Close()
	for _, p := range pairs {
		if p.secret == "" || security.IsEncrypted(p.secret) {
			continue
		}
		enc, eerr := security.EncryptWithMaster(p.secret)
		if eerr != nil {
			return eerr
		}
		if _, uerr := d.db.Exec(`UPDATE webhook_subscriptions SET secret = ? WHERE id = ?`, enc, p.id); uerr != nil {
			return uerr
		}
	}
	return nil
}

func (d *Database) DeleteWebhookSubscription(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`DELETE FROM webhook_outbox WHERE subscription_id = ?`, id)
	_, err := d.db.Exec(`DELETE FROM webhook_subscriptions WHERE id = ?`, id)
	return err
}

// EnqueueWebhook inserts outbox rows for every active subscription that matches
// the event and (optionally) project. Called instead of direct HTTP send.
//
// IMPORTANT: ListWebhookSubscriptions takes its own read lock, so it must be
// called BEFORE acquiring d.mu. Taking the write lock first and then calling a
// method that takes the read lock would deadlock Go's RWMutex (it is not
// re-entrant). We therefore snapshot the (plaintext) subscriptions first, then
// take the write lock only around the outbox inserts.
func (d *Database) EnqueueWebhook(event, payload, projectSlug string) error {
	// Snapshot subscriptions (plaintext secrets) under the method's own read lock.
	subs, err := d.ListWebhookSubscriptions()
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range subs {
		if !s.IsActive {
			continue
		}
		if !eventMatches(s.Events, event) {
			continue
		}
		if s.ProjectSlug != "" && s.ProjectSlug != projectSlug {
			continue
		}
		if _, err := d.db.Exec(
			`INSERT INTO webhook_outbox (subscription_id, url, payload, secret, attempts, next_at) VALUES (?, ?, ?, ?, 0, ?)`,
			s.ID, s.URL, payload, s.Secret, now); err != nil {
			return err
		}
	}
	return nil
}

// GetDueOutbox returns outbox rows whose next_at <= now, capped at limit.
func (d *Database) GetDueOutbox(now int64, limit int) ([]WebhookOutbox, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT id, subscription_id, url, payload, secret, attempts, next_at, last_error, created_at FROM webhook_outbox WHERE next_at <= ? ORDER BY next_at ASC LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []WebhookOutbox
	for rows.Next() {
		var o WebhookOutbox
		var createdAt int64
		if err := rows.Scan(&o.ID, &o.SubscriptionID, &o.URL, &o.Payload, &o.Secret, &o.Attempts, &o.NextAt, &o.LastError, &createdAt); err != nil {
			return nil, err
		}
		o.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, o)
	}
	return list, nil
}

// MarkOutboxSuccess deletes a successfully delivered outbox row.
func (d *Database) MarkOutboxSuccess(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM webhook_outbox WHERE id = ?`, id)
	return err
}

// MarkOutboxFailure records an attempt and schedules the next retry with exponential backoff.
// Stops retrying after maxAttempts.
func (d *Database) MarkOutboxFailure(id int64, lastErr string, attempts int, nextAt int64, maxAttempts int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if attempts >= maxAttempts {
		_, err := d.db.Exec(`DELETE FROM webhook_outbox WHERE id = ?`, id)
		return err
	}
	_, err := d.db.Exec(`UPDATE webhook_outbox SET attempts = ?, last_error = ?, next_at = ? WHERE id = ?`, attempts, lastErr, nextAt, id)
	return err
}

// WebhookDeliveryStats aggregates delivery outcomes for a subscription: total
// attempts, successful (HTTP 2xx) vs failed (anything else, including a 0
// status recorded when the request never completed). rate is the success
// percentage (0 when there are no deliveries).
func (d *Database) WebhookDeliveryStats(subID int64) (total, success, failed int, rate float64, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	row := d.db.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN response_status >= 200 AND response_status < 300 THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN response_status < 200 OR response_status >= 300 THEN 1 ELSE 0 END), 0)
		 FROM webhook_deliveries WHERE subscription_id = ?`, subID)
	if e := row.Scan(&total, &success, &failed); e != nil {
		return 0, 0, 0, 0, e
	}
	if total > 0 {
		rate = float64(success) / float64(total) * 100
	}
	return total, success, failed, rate, nil
}

// RecordWebhookDelivery logs a single webhook delivery attempt (success or
// failure) for audit/history display. Response bodies and error text are
// truncated to keep the table bounded.
func (d *Database) RecordWebhookDelivery(subID int64, event, url, requestBody string, responseStatus int, responseBody, errText string, createdAt int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	const maxLen = 4096
	if len(responseBody) > maxLen {
		responseBody = responseBody[:maxLen]
	}
	if len(errText) > maxLen {
		errText = errText[:maxLen]
	}
	_, err := d.db.Exec(
		`INSERT INTO webhook_deliveries (subscription_id, event, url, request_body, response_status, response_body, error, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		subID, event, url, requestBody, responseStatus, responseBody, errText, createdAt)
	return err
}

// ListWebhookDeliveries returns the most recent delivery records for a subscription.
func (d *Database) ListWebhookDeliveries(subID int64, limit int) ([]WebhookDelivery, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := d.db.Query(`SELECT id, subscription_id, event, url, request_body, response_status, response_body, error, created_at FROM webhook_deliveries WHERE subscription_id = ? ORDER BY created_at DESC LIMIT ?`, subID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []WebhookDelivery
	for rows.Next() {
		var dl WebhookDelivery
		var createdAt int64
		if err := rows.Scan(&dl.ID, &dl.SubscriptionID, &dl.Event, &dl.URL, &dl.RequestBody, &dl.ResponseStatus, &dl.ResponseBody, &dl.Error, &createdAt); err != nil {
			return nil, err
		}
		dl.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, dl)
	}
	return list, nil
}
