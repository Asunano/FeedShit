package database

import (
	"database/sql"
	"time"
)

// InsertFeedbackNote adds a note/reply to a feedback.
func (d *Database) InsertFeedbackNote(feedbackID int64, content, author string, isPublic bool, filePaths string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	pub := 0
	if isPublic {
		pub = 1
	}
	res, err := d.db.Exec(
		`INSERT INTO feedback_notes (feedback_id, content, author, is_public, file_paths) VALUES (?, ?, ?, ?, ?)`,
		feedbackID, content, author, pub, filePaths,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListFeedbackNotes returns all notes for a feedback, ordered by creation time.
func (d *Database) ListFeedbackNotes(feedbackID int64) ([]FeedbackNote, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(
		`SELECT id, feedback_id, content, author, is_public, file_paths, created_at FROM feedback_notes WHERE feedback_id = ? ORDER BY created_at ASC`,
		feedbackID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []FeedbackNote
	for rows.Next() {
		var n FeedbackNote
		var createdAt int64
		var isPublic int
		if err := rows.Scan(&n.ID, &n.FeedbackID, &n.Content, &n.Author, &isPublic, &n.FilePaths, &createdAt); err != nil {
			return nil, err
		}
		n.IsPublic = isPublic == 1
		n.CreatedAt = time.Unix(createdAt, 0)
		notes = append(notes, n)
	}
	return notes, nil
}

// GetFeedbackNote returns a single note by ID, or nil if not found.
func (d *Database) GetFeedbackNote(id int64) (*FeedbackNote, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	row := d.db.QueryRow(
		`SELECT id, feedback_id, content, author, is_public, file_paths, created_at FROM feedback_notes WHERE id = ?`,
		id,
	)
	var n FeedbackNote
	var createdAt int64
	var isPublic int
	if err := row.Scan(&n.ID, &n.FeedbackID, &n.Content, &n.Author, &isPublic, &n.FilePaths, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	n.IsPublic = isPublic == 1
	n.CreatedAt = time.Unix(createdAt, 0)
	return &n, nil
}

// DeleteFeedbackNote removes a note by ID.
func (d *Database) DeleteFeedbackNote(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`DELETE FROM feedback_notes WHERE id = ?`, id)
	return err
}
