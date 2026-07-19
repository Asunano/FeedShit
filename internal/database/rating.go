package database

import (
	"database/sql"
	"time"
)

// UpsertRating writes (or overwrites) a CSAT rating for a feedback.
func (d *Database) UpsertRating(feedbackID int64, score int, comment string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`
		INSERT INTO feedback_ratings (feedback_id, score, comment) VALUES (?, ?, ?)
		ON CONFLICT(feedback_id) DO UPDATE SET score = excluded.score, comment = excluded.comment`,
		feedbackID, score, comment)
	return err
}

// GetRating returns the CSAT rating for a feedback, or nil if none.
func (d *Database) GetRating(feedbackID int64) (*FeedbackRating, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var r FeedbackRating
	var createdAt int64
	err := d.db.QueryRow(`SELECT feedback_id, score, comment, created_at FROM feedback_ratings WHERE feedback_id = ?`, feedbackID).
		Scan(&r.FeedbackID, &r.Score, &r.Comment, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt = time.Unix(createdAt, 0)
	return &r, nil
}

// GetCSATStats returns overall average and per-assignee average scores.
func (d *Database) GetCSATStats() (avg float64, total int, byAssignee map[string]float64, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	byAssignee = map[string]float64{}

	row := d.db.QueryRow(`SELECT COUNT(*), COALESCE(ROUND(AVG(score), 2), 0) FROM feedback_ratings`)
	if e := row.Scan(&total, &avg); e != nil {
		return 0, 0, byAssignee, e
	}

	rows, e := d.db.Query(`
		SELECT COALESCE(f.assignee, ''), COALESCE(ROUND(AVG(r.score), 2), 0), COUNT(*)
		FROM feedback_ratings r JOIN feedbacks f ON f.id = r.feedback_id
		WHERE f.assignee != ''
		GROUP BY f.assignee`)
	if e != nil {
		return avg, total, byAssignee, nil
	}
	defer rows.Close()
	for rows.Next() {
		var who string
		var a float64
		var c int
		if err := rows.Scan(&who, &a, &c); err != nil {
			continue
		}
		byAssignee[who] = a
	}
	return avg, total, byAssignee, nil
}
