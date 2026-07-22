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

	where := `WHERE f.public_on_roadmap = 1 AND f.is_duplicate = 0`
	args := []interface{}{}
	if projectSlug != "" {
		where += ` AND f.project_id = ?`
		args = append(args, projectSlug)
	}
	if category != "" {
		where += ` AND f.category = ?`
		args = append(args, category)
	}
	if projectSlug == "" {
		// Global roadmap (no project slug): only include projects that have
		// been explicitly opted in via the admin project settings.
		where += ` AND f.project_id IN (SELECT slug FROM projects WHERE show_on_global_roadmap = 1)`
	}
	rows, err := d.db.Query(`
		SELECT f.id, f.title, f.category, f.description, f.project_id, f.roadmap_status,
		       COALESCE(v.cnt, 0), f.created_at,
		       COALESCE(m.c, 0) AS mention_count,
		       f.roadmap_order, f.roadmap_target_date, f.roadmap_owner, f.roadmap_release
		FROM feedbacks f
		LEFT JOIN (SELECT feedback_id, COUNT(*) cnt FROM feedback_votes WHERE target_type = 'feedback' GROUP BY feedback_id) v ON v.feedback_id = f.id
		LEFT JOIN (SELECT duplicate_of, COUNT(*) c FROM feedbacks WHERE is_duplicate = 1 AND duplicate_of != 0 GROUP BY duplicate_of) m ON m.duplicate_of = f.id
		`+where+`
		ORDER BY f.roadmap_order DESC, v.cnt DESC, f.created_at DESC
		LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []RoadmapItem
	for rows.Next() {
		var it RoadmapItem
		var createdAt int64
		var targetDate int64
		if err := rows.Scan(&it.ID, &it.Title, &it.Category, &it.Description, &it.ProjectSlug, &it.RoadmapStatus, &it.Votes, &createdAt, &it.MentionCount, &it.RoadmapOrder, &targetDate, &it.Owner, &it.Release); err != nil {
			return nil, err
		}
		it.CreatedAt = time.Unix(createdAt, 0)
		it.TargetDate = targetDate
		items = append(items, it)
	}
	return items, nil
}

// CountPublicRoadmap returns the total number of public roadmap items matching
// the same filters, used to drive pagination on the board.
func (d *Database) CountPublicRoadmap(projectSlug, category string) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

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
	if projectSlug == "" {
		where += ` AND project_id IN (SELECT slug FROM projects WHERE show_on_global_roadmap = 1)`
	}
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks `+where, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ListRoadmapForAdmin returns all feedbacks that are on the roadmap board
// (roadmap_status set) or flagged public, ordered by board stage then recency.
// Used by the admin roadmap management tab to toggle status / public visibility.
func (d *Database) ListRoadmapForAdmin(limit, offset int) ([]RoadmapAdminItem, int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	where := `WHERE (f.roadmap_status != '' OR f.public_on_roadmap = 1) AND f.is_duplicate = 0`

	var total int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks f `+where).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := d.db.Query(`
		SELECT f.id, f.title, f.category, f.project_id, f.roadmap_status, f.public_on_roadmap,
		       COALESCE(v.cnt, 0), f.updated_at,
		       COALESCE(m.c, 0) AS mention_count,
		       f.roadmap_order, f.roadmap_target_date, f.roadmap_owner, f.roadmap_release
		FROM feedbacks f
		LEFT JOIN (SELECT feedback_id, COUNT(*) cnt FROM feedback_votes WHERE target_type = 'feedback' GROUP BY feedback_id) v ON v.feedback_id = f.id
		LEFT JOIN (SELECT duplicate_of, COUNT(*) c FROM feedbacks WHERE is_duplicate = 1 AND duplicate_of != 0 GROUP BY duplicate_of) m ON m.duplicate_of = f.id
		`+where+`
		ORDER BY f.roadmap_order DESC,
		         CASE f.roadmap_status WHEN 'in_progress' THEN 1 WHEN 'released' THEN 2 ELSE 0 END,
		         f.updated_at DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var items []RoadmapAdminItem
	for rows.Next() {
		var it RoadmapAdminItem
		var pub int
		var targetDate int64
		if err := rows.Scan(&it.ID, &it.Title, &it.Category, &it.ProjectSlug, &it.RoadmapStatus, &pub, &it.Votes, &it.UpdatedAt, &it.MentionCount, &it.RoadmapOrder, &targetDate, &it.Owner, &it.Release); err != nil {
			return nil, 0, err
		}
		it.PublicOnRoadmap = pub == 1
		it.TargetDate = targetDate
		items = append(items, it)
	}
	return items, total, nil
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

// SetRoadmapMeta updates the curation fields (sorting order, target date,
// owner, release version) without touching public/status visibility.
func (d *Database) SetRoadmapMeta(feedbackID int64, order int, targetDate int64, owner, release string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE feedbacks SET roadmap_order = ?, roadmap_target_date = ?, roadmap_owner = ?, roadmap_release = ?, updated_at = strftime('%s','now') WHERE id = ?`, order, targetDate, owner, release, feedbackID)
	return err
}

// RoadmapConfig holds the global roadmap automation settings, read from the
// config table with safe defaults when a key is absent.
type RoadmapConfig struct {
	AutoBoard         bool
	DefaultStatus     string
	DefaultPublic     bool
	AutoPromote       bool
	AutoPromoteStatus string
}

// GetRoadmapConfig reads the roadmap automation settings. Missing keys fall
// back to safe defaults (auto-board on, default status planning, default
// public off, auto-promote on, promote to released). Absent boolean keys are
// treated as enabled so the feature works even before InitDefaultConfig seeds.
func (d *Database) GetRoadmapConfig() RoadmapConfig {
	rc := RoadmapConfig{AutoBoard: true, DefaultStatus: "planning", DefaultPublic: false, AutoPromote: true, AutoPromoteStatus: "released"}
	if v := d.GetConfig("roadmap_auto_board"); v == "false" {
		rc.AutoBoard = false
	}
	if v := d.GetConfig("roadmap_default_status"); v != "" {
		rc.DefaultStatus = v
	}
	if v := d.GetConfig("roadmap_default_public"); v == "true" {
		rc.DefaultPublic = true
	}
	if v := d.GetConfig("roadmap_auto_promote"); v == "false" {
		rc.AutoPromote = false
	}
	if v := d.GetConfig("roadmap_auto_promote_status"); v != "" {
		rc.AutoPromoteStatus = v
	}
	return rc
}

// GetRoadmapState returns the current board status and public flag for a
// feedback, used by auto-promote to decide whether promotion applies.
func (d *Database) GetRoadmapState(feedbackID int64) (status string, public bool, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var s string
	var p int
	if err = d.db.QueryRow(`SELECT roadmap_status, public_on_roadmap FROM feedbacks WHERE id = ?`, feedbackID).Scan(&s, &p); err != nil {
		return "", false, err
	}
	return s, p == 1, nil
}
