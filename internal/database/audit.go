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

// PruneAuditLogs deletes audit log entries older than the specified number of days.
// Returns the number of deleted rows.
func (d *Database) PruneAuditLogs(days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	res, err := d.db.Exec(`DELETE FROM audit_logs WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
