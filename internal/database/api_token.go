package database

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"
)

// hashTokenSHA256 returns the SHA-256 hex digest of a token string.
// This is used to avoid storing raw tokens at rest — a DB leak cannot recover
// usable tokens, only their hashes.
func hashTokenSHA256(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// maskToken returns a truncated/masked version of a token for display.
// Shows first 8 chars + "...".
func maskToken(token string) string {
	if len(token) <= 8 {
		return token[:len(token)/2] + "..."
	}
	return token[:8] + "..."
}

func (d *Database) CreateAPIToken(token, name, projectID string, rateLimit, quotaPerDay int) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Store the SHA-256 hash of the token — never the raw value.
	res, err := d.db.Exec(
		`INSERT INTO api_tokens (token, name, project_id, rate_limit, quota_per_day) VALUES (?, ?, ?, ?, ?)`,
		hashTokenSHA256(token), name, projectID, rateLimit, quotaPerDay)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *Database) ListAPITokens() ([]APIToken, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT id, token, name, project_id, is_active, rate_limit, quota_per_day, COALESCE(last_used_at, ''), created_at FROM api_tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []APIToken
	for rows.Next() {
		var t APIToken
		var isActive int
		var createdAt int64
		if err := rows.Scan(&t.ID, &t.Token, &t.Name, &t.ProjectID, &isActive, &t.RateLimit, &t.QuotaPerDay, &t.LastUsedAt, &createdAt); err != nil {
			return nil, err
		}
		t.IsActive = isActive == 1
		t.CreatedAt = time.Unix(createdAt, 0)
		// Mask the hash so the list API never exposes usable tokens
		t.Token = maskToken(t.Token)
		list = append(list, t)
	}
	return list, nil
}

func (d *Database) GetAPITokenByToken(token string) (*APIToken, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var t APIToken
	var isActive int
	var createdAt int64
	hash := hashTokenSHA256(token)
	err := d.db.QueryRow(`SELECT id, token, name, project_id, is_active, rate_limit, quota_per_day, COALESCE(last_used_at, ''), created_at FROM api_tokens WHERE token = ?`, hash).
		Scan(&t.ID, &t.Token, &t.Name, &t.ProjectID, &isActive, &t.RateLimit, &t.QuotaPerDay, &t.LastUsedAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.IsActive = isActive == 1
	t.CreatedAt = time.Unix(createdAt, 0)
	if !t.IsActive {
		return nil, nil
	}
	return &t, nil
}

func (d *Database) UpdateAPIToken(id int64, name, projectID string, isActive *bool, rateLimit, quotaPerDay *int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	setClauses := []string{}
	args := []interface{}{}

	if name != "" {
		setClauses = append(setClauses, "name = ?")
		args = append(args, name)
	}
	if projectID != "" {
		setClauses = append(setClauses, "project_id = ?")
		args = append(args, projectID)
	}
	if isActive != nil {
		active := 0
		if *isActive {
			active = 1
		}
		setClauses = append(setClauses, "is_active = ?")
		args = append(args, active)
	}
	if rateLimit != nil {
		setClauses = append(setClauses, "rate_limit = ?")
		args = append(args, *rateLimit)
	}
	if quotaPerDay != nil {
		setClauses = append(setClauses, "quota_per_day = ?")
		args = append(args, *quotaPerDay)
	}

	if len(setClauses) == 0 {
		return nil
	}

	args = append(args, id)
	_, err := d.db.Exec(`UPDATE api_tokens SET `+strings.Join(setClauses, ", ")+` WHERE id = ?`, args...)
	return err
}

func (d *Database) DeleteAPIToken(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM api_tokens WHERE id = ?`, id)
	return err
}

func (d *Database) TouchAPIToken(token string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`UPDATE api_tokens SET last_used_at = strftime('%s', 'now') WHERE token = ?`, hashTokenSHA256(token))
}

// RecordTokenUsage enforces the daily quota for an API token. It atomically
// increments today's usage counter (resetting when the calendar date changes)
// and returns false if the configured quota (quotaPerDay > 0) has been reached.
func (d *Database) RecordTokenUsage(token string, quotaPerDay int) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	hash := hashTokenSHA256(token)
	today := time.Now().Format("2006-01-02")
	var used int
	var date string
	if err := d.db.QueryRow(`SELECT daily_count, daily_date FROM api_tokens WHERE token = ?`, hash).Scan(&used, &date); err != nil {
		return false, err
	}
	if date != today {
		used = 0
	}
	if quotaPerDay > 0 && used >= quotaPerDay {
		return false, nil
	}
	if date != today {
		_, err := d.db.Exec(`UPDATE api_tokens SET daily_count = 1, daily_date = ? WHERE token = ?`, today, hash)
		return err == nil, err
	}
	_, err := d.db.Exec(`UPDATE api_tokens SET daily_count = daily_count + 1 WHERE token = ?`, hash)
	return err == nil, err
}
