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

// bulkUpdateFeedbackField updates a single column to the same value across the
// given feedback IDs, taking the shared write lock and touching updated_at. The
// public BulkUpdateFeedback* wrappers delegate to this so the IN-clause
// construction and exec boilerplate live in exactly one place. field is a
// hardcoded column name (never user input), so string interpolation is safe.
func (d *Database) bulkUpdateFeedbackField(ids []int64, field, value string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(ids) == 0 {
		return 0, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, value)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	query := `UPDATE feedbacks SET ` + field + ` = ?, updated_at = strftime('%s', 'now') WHERE id IN (` + strings.Join(ph, ",") + `)`
	res, err := d.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BulkUpdateFeedbackStatus updates status for multiple feedbacks.
func (d *Database) BulkUpdateFeedbackStatus(ids []int64, status string) (int64, error) {
	return d.bulkUpdateFeedbackField(ids, "status", status)
}

// BulkUpdateFeedbackTags updates tags for multiple feedbacks.
func (d *Database) BulkUpdateFeedbackTags(ids []int64, tags string) (int64, error) {
	return d.bulkUpdateFeedbackField(ids, "tags", tags)
}

// BulkUpdateFeedbackAssignee updates assignee for multiple feedbacks.
func (d *Database) BulkUpdateFeedbackAssignee(ids []int64, assignee string) (int64, error) {
	return d.bulkUpdateFeedbackField(ids, "assignee", assignee)
}

// BulkUpdateFeedbackPriority updates priority for multiple feedbacks.
func (d *Database) BulkUpdateFeedbackPriority(ids []int64, priority string) (int64, error) {
	return d.bulkUpdateFeedbackField(ids, "priority", priority)
}
