package database

import (
	"time"
)

// GetPublicRoadmap returns public, non-duplicate feedbacks for a project,
// ordered by votes then recency. Sensitive fields (IP, contact, internal notes) excluded.
func (d *Database) GetPublicRoadmap(projectSlug string, category string, limit, offset int) ([]RoadmapItem, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	where := `WHERE public_on_roadmap = 1 AND is_duplicate = 0`
	args := []interface{}{}
	if projectSlug != "" {
		where += ` AND project_id = ?`
		args = append(args, projectSlug)
	}
	if category != "" {
		where += ` AND category = ?`
		args = append(args, category)
	}
	rows, err := d.db.Query(`
		SELECT f.id, f.title, f.category, f.project_id, f.roadmap_status, COALESCE(v.cnt, 0), f.created_at
		FROM feedbacks f
		LEFT JOIN (SELECT feedback_id, COUNT(*) cnt FROM feedback_votes GROUP BY feedback_id) v ON v.feedback_id = f.id
		`+where+`
		ORDER BY v.cnt DESC, f.created_at DESC
		LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []RoadmapItem
	for rows.Next() {
		var it RoadmapItem
		var createdAt int64
		if err := rows.Scan(&it.ID, &it.Title, &it.Category, &it.ProjectSlug, &it.RoadmapStatus, &it.Votes, &createdAt); err != nil {
			return nil, err
		}
		it.CreatedAt = time.Unix(createdAt, 0)
		items = append(items, it)
	}
	return items, nil
}

// SetRoadmap toggles public visibility and/or board status of a feedback.
func (d *Database) SetRoadmap(feedbackID int64, public bool, status string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	pub := 0
	if public {
		pub = 1
	}
	if status == "" {
		_, err := d.db.Exec(`UPDATE feedbacks SET public_on_roadmap = ?, updated_at = strftime('%s','now') WHERE id = ?`, pub, feedbackID)
		return err
	}
	_, err := d.db.Exec(`UPDATE feedbacks SET public_on_roadmap = ?, roadmap_status = ?, updated_at = strftime('%s','now') WHERE id = ?`, pub, status, feedbackID)
	return err
}
