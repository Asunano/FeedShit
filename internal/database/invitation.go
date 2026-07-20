package database

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// InvitationToken represents a one-time or limited-use registration link.
type InvitationToken struct {
	ID         int64  `json:"id"`
	Token      string `json:"token"`
	Role       string `json:"role"`
	ProjectIDs string `json:"project_ids"` // JSON array of project slugs
	MaxUses    int    `json:"max_uses"`
	UsedCount  int    `json:"used_count"`
	CreatedBy  string `json:"created_by"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
}

// CreateInvitation generates and stores a new invitation token.
func (d *Database) CreateInvitation(role string, projectIDs []string, maxUses int, createdBy string, expiresInDays int) (*InvitationToken, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("failed to generate invitation token: %w", err)
	}
	token := hex.EncodeToString(b)

	projJSON, _ := json.Marshal(projectIDs)
	expiresAt := int64(0)
	if expiresInDays > 0 {
		expiresAt = time.Now().AddDate(0, 0, expiresInDays).Unix()
	}

	_, err := d.db.Exec(
		`INSERT INTO invitation_tokens (token, role, project_ids, max_uses, created_by, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		token, role, string(projJSON), maxUses, createdBy, expiresAt)
	if err != nil {
		return nil, err
	}

	return &InvitationToken{
		Token:      token,
		Role:       role,
		ProjectIDs: string(projJSON),
		MaxUses:    maxUses,
		CreatedBy:  createdBy,
		ExpiresAt:  expiresAt,
	}, nil
}

// ValidateInvitation checks if a token is valid and not exhausted.
func (d *Database) ValidateInvitation(token string) (*InvitationToken, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var inv InvitationToken
	var expiresAt int64
	err := d.db.QueryRow(
		`SELECT id, token, role, project_ids, max_uses, used_count, created_by, created_at, expires_at
		 FROM invitation_tokens WHERE token = ?`, token,
	).Scan(&inv.ID, &inv.Token, &inv.Role, &inv.ProjectIDs, &inv.MaxUses, &inv.UsedCount, &inv.CreatedBy, &inv.CreatedAt, &expiresAt)
	if err != nil {
		return nil, err
	}
	inv.ExpiresAt = expiresAt

	if inv.MaxUses > 0 && inv.UsedCount >= inv.MaxUses {
		return nil, fmt.Errorf("邀请链接已失效（已达使用上限）")
	}
	if expiresAt > 0 && time.Now().Unix() > expiresAt {
		return nil, fmt.Errorf("邀请链接已过期")
	}
	return &inv, nil
}

// UseInvitation increments the usage count of an invitation token.
func (d *Database) UseInvitation(token string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE invitation_tokens SET used_count = used_count + 1 WHERE token = ?`, token)
	return err
}

// ListInvitations returns all invitation tokens.
func (d *Database) ListInvitations() ([]InvitationToken, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT id, token, role, project_ids, max_uses, used_count, created_by, created_at, expires_at FROM invitation_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []InvitationToken
	for rows.Next() {
		var inv InvitationToken
		var expiresAt int64
		if err := rows.Scan(&inv.ID, &inv.Token, &inv.Role, &inv.ProjectIDs, &inv.MaxUses, &inv.UsedCount, &inv.CreatedBy, &inv.CreatedAt, &expiresAt); err != nil {
			return nil, err
		}
		inv.ExpiresAt = expiresAt
		list = append(list, inv)
	}
	if list == nil {
		list = []InvitationToken{}
	}
	return list, nil
}
