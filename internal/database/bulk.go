package database

import (
	"strings"
)

// BulkDeleteFeedbacks deletes multiple feedbacks by ID.
func (d *Database) BulkDeleteFeedbacks(ids []int64) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	// Cascade delete notes first
	inClause := strings.Join(placeholders, ",")
	d.db.Exec(`DELETE FROM feedback_notes WHERE feedback_id IN (`+inClause+`)`, args...)
	query := `DELETE FROM feedbacks WHERE id IN (` + inClause + `)`
	res, err := d.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BulkUpdateFeedbackStatus updates status for multiple feedbacks.
func (d *Database) BulkUpdateFeedbackStatus(ids []int64, status string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, status)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := `UPDATE feedbacks SET status = ?, updated_at = strftime('%s', 'now') WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	res, err := d.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BulkUpdateFeedbackTags updates tags for multiple feedbacks.
func (d *Database) BulkUpdateFeedbackTags(ids []int64, tags string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(ids) == 0 {
		return 0, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, tags)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	res, err := d.db.Exec(`UPDATE feedbacks SET tags = ?, updated_at = strftime('%s', 'now') WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BulkUpdateFeedbackAssignee updates assignee for multiple feedbacks.
func (d *Database) BulkUpdateFeedbackAssignee(ids []int64, assignee string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(ids) == 0 {
		return 0, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, assignee)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	res, err := d.db.Exec(`UPDATE feedbacks SET assignee = ?, updated_at = strftime('%s', 'now') WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BulkUpdateFeedbackPriority updates priority for multiple feedbacks.
func (d *Database) BulkUpdateFeedbackPriority(ids []int64, priority string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(ids) == 0 {
		return 0, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, priority)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	res, err := d.db.Exec(`UPDATE feedbacks SET priority = ?, updated_at = strftime('%s', 'now') WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
