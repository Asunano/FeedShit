package database

import (
	"database/sql"
	"time"
)

// execer is satisfied by both *sql.DB and *sql.Tx, letting ImportFeedback run
// inside an external transaction when callers need atomic bulk inserts.
type execer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

func (d *Database) importFeedbackExec(e execer, f *Feedback, createdAtUnix int64) (int64, error) {
	status := f.Status
	if status == "" {
		status = "pending"
	}
	ts := createdAtUnix
	if ts <= 0 {
		ts = time.Now().Unix()
	}
	res, err := e.Exec(
		`INSERT INTO feedbacks (project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, category, content_hash, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ProjectID, f.Title, f.Description, f.CustomData, f.FilePaths, f.ClientIP, status, f.Tags, f.Assignee, f.ContactName, f.ContactEmail, f.TrackingToken, f.Priority, f.Category, ComputeContentHash(f.Title, f.Description), ts, ts,
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

// ImportFeedback imports a feedback with a specific creation timestamp (for CSV import).
func (d *Database) ImportFeedback(f *Feedback, createdAtUnix int64) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.importFeedbackExec(d.db, f, createdAtUnix)
}

// BeginTx starts a transaction so a bulk import can roll back the whole batch
// on a mid-stream failure instead of leaving partial rows committed.
func (d *Database) BeginTx() (*sql.Tx, error) {
	return d.db.Begin()
}

// ImportFeedbackTx imports a feedback inside an existing transaction. It does
// NOT take d.mu: the transaction already serializes access via its dedicated
// connection, and locking here would deadlock with other ops that hold d.mu
// while waiting for that same connection.
func (d *Database) ImportFeedbackTx(tx *sql.Tx, f *Feedback, createdAtUnix int64) (int64, error) {
	return d.importFeedbackExec(tx, f, createdAtUnix)
}
