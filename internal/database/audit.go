package database

import (
	"time"
)

// InsertAuditLog inserts a new audit log entry.
func (d *Database) InsertAuditLog(action, detail, user, ip string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(
		`INSERT INTO audit_logs (action, detail, user, ip) VALUES (?, ?, ?, ?)`,
		action, detail, user, ip,
	)
	return err
}

// ListAuditLogs returns recent audit log entries.
func (d *Database) ListAuditLogs(limit, offset int) ([]AuditLog, int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var total int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := d.db.Query(
		`SELECT id, action, detail, user, ip, created_at FROM audit_logs ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []AuditLog
	for rows.Next() {
		var a AuditLog
		var createdAt int64
		if err := rows.Scan(&a.ID, &a.Action, &a.Detail, &a.User, &a.IP, &createdAt); err != nil {
			return nil, 0, err
		}
		a.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, a)
	}
	return list, total, nil
}
