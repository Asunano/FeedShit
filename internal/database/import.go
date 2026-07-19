package database

import (
	"time"
)

// ImportFeedback imports a feedback with a specific creation timestamp (for CSV import).
func (d *Database) ImportFeedback(f *Feedback, createdAtUnix int64) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	status := f.Status
	if status == "" {
		status = "pending"
	}
	ts := createdAtUnix
	if ts <= 0 {
		ts = time.Now().Unix()
	}
	res, err := d.db.Exec(
		`INSERT INTO feedbacks (project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, category, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ProjectID, f.Title, f.Description, f.CustomData, f.FilePaths, f.ClientIP, status, f.Tags, f.Assignee, f.ContactName, f.ContactEmail, f.TrackingToken, f.Priority, f.Category, ts,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	f.ID = id
	return id, nil
}
