package database

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CreateProject inserts a new project and returns its ID.
func (d *Database) CreateProject(p *Project) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	active := 0
	if p.IsActive {
		active = 1
	}
	archived := 0
	if p.IsArchived {
		archived = 1
	}
	showGlobal := 0
	if p.ShowOnGlobalRoadmap {
		showGlobal = 1
	}
	res, err := d.db.Exec(
		`INSERT INTO projects (name, slug, description, is_active, is_archived, form_schema, announcement, show_on_global_roadmap) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Slug, p.Description, active, archived, p.FormSchema, p.Announcement, showGlobal,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	p.ID = id
	p.CreatedAt = time.Now()
	return id, nil
}

// UpdateProject updates an existing project.
func (d *Database) UpdateProject(p *Project) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	active := 0
	if p.IsActive {
		active = 1
	}
	archived := 0
	if p.IsArchived {
		archived = 1
	}
	showGlobal := 0
	if p.ShowOnGlobalRoadmap {
		showGlobal = 1
	}
	_, err := d.db.Exec(
		`UPDATE projects SET name = ?, slug = ?, description = ?, is_active = ?, is_archived = ?, form_schema = ?, announcement = ?, show_on_global_roadmap = ? WHERE id = ?`,
		p.Name, p.Slug, p.Description, active, archived, p.FormSchema, p.Announcement, showGlobal, p.ID,
	)
	return err
}

// DeleteProject removes a project and all associated data (cascade).
func (d *Database) DeleteProject(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// First get the project slug to delete associated data
	var slug string
	err := d.db.QueryRow(`SELECT slug FROM projects WHERE id = ?`, id).Scan(&slug)
	if err != nil {
		return err
	}

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Get feedback IDs for this project to clean up dependent tables
	rows, err := tx.Query(`SELECT id FROM feedbacks WHERE project_id = ?`, slug)
	if err != nil {
		return err
	}
	var fbIDs []int64
	for rows.Next() {
		var fid int64
		if err := rows.Scan(&fid); err != nil {
			rows.Close()
			return err
		}
		fbIDs = append(fbIDs, fid)
	}
	rows.Close()

	// Delete feedback-dependent data (notes, votes, ratings, status history)
	if len(fbIDs) > 0 {
		placeholders := make([]string, len(fbIDs))
		args := make([]interface{}, len(fbIDs))
		for i, fid := range fbIDs {
			placeholders[i] = "?"
			args[i] = fid
		}
		inClause := strings.Join(placeholders, ",")
		for _, table := range []string{"feedback_notes", "feedback_votes", "feedback_ratings", "feedback_status_history"} {
			if _, err := tx.Exec(`DELETE FROM `+table+` WHERE feedback_id IN (`+inClause+`)`, args...); err != nil {
				return err
			}
		}
	}

	// Delete feedbacks
	if _, err := tx.Exec(`DELETE FROM feedbacks WHERE project_id = ?`, slug); err != nil {
		return err
	}

	// Delete project-scoped data
	for _, table := range []string{"categories", "webhook_subscriptions", "member_grants"} {
		col := "project_slug"
		if table == "webhook_subscriptions" {
			col = "project_id"
		}
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE `+col+` = ?`, slug); err != nil {
			return err
		}
	}

	// Delete webhook outbox entries for this project's subscriptions
	if _, err := tx.Exec(`DELETE FROM webhook_outbox WHERE subscription_id NOT IN (SELECT id FROM webhook_subscriptions)`); err != nil {
		return err
	}

	// Delete the project
	if _, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, id); err != nil {
		return err
	}

	return tx.Commit()
}

// CloneProject duplicates an existing project into a new one. It copies the
// form schema, description, announcement and all categories, but resets the
// clone to active + not-archived and does NOT copy feedbacks/files (those are
// keyed by slug and reference the old project only). The caller is responsible
// for ensuring the new slug is unique before calling this.
func (d *Database) CloneProject(srcID int64, newName, newSlug string) (int64, error) {
	// Read source project + categories OUTSIDE the write lock (those reads take
	// their own RLock; nesting them inside a write lock would deadlock the
	// non-reentrant RWMutex).
	src, err := d.GetProject(srcID)
	if err != nil {
		return 0, err
	}
	if src == nil {
		return 0, fmt.Errorf("源项目不存在")
	}
	cats, err := d.ListCategories(src.Slug)
	if err != nil {
		return 0, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	showGlobal := 0
	if src.ShowOnGlobalRoadmap {
		showGlobal = 1
	}
	res, err := d.db.Exec(
		`INSERT INTO projects (name, slug, description, is_active, is_archived, form_schema, announcement, show_on_global_roadmap) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		newName, newSlug, src.Description, 1, 0, src.FormSchema, src.Announcement, showGlobal,
	)
	if err != nil {
		return 0, err
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, c := range cats {
		if _, err := d.db.Exec(
			`INSERT INTO categories (project_slug, key, name, color, sort_order) VALUES (?, ?, ?, ?, ?)`,
			newSlug, c.Key, c.Name, c.Color, c.SortOrder,
		); err != nil {
			return newID, err
		}
	}
	return newID, nil
}

// GetProject returns a project by ID.
func (d *Database) GetProject(id int64) (*Project, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var p Project
	var createdAt int64
	var isActive, isArchived, showGlobal int
	err := d.db.QueryRow(
		`SELECT id, name, slug, description, is_active, is_archived, form_schema, announcement, show_on_global_roadmap, created_at FROM projects WHERE id = ?`, id,
	).Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &isActive, &isArchived, &p.FormSchema, &p.Announcement, &showGlobal, &createdAt)
	if err != nil {
		return nil, err
	}
	p.IsActive = isActive == 1
	p.IsArchived = isArchived == 1
	p.ShowOnGlobalRoadmap = showGlobal == 1
	p.CreatedAt = time.Unix(createdAt, 0)
	return &p, nil
}

// GetProjectBySlug returns a project by its slug.
func (d *Database) GetProjectBySlug(slug string) (*Project, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var p Project
	var createdAt int64
	var isActive, isArchived, showGlobal int
	err := d.db.QueryRow(
		`SELECT id, name, slug, description, is_active, is_archived, form_schema, announcement, show_on_global_roadmap, created_at FROM projects WHERE slug = ?`, slug,
	).Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &isActive, &isArchived, &p.FormSchema, &p.Announcement, &showGlobal, &createdAt)
	if err != nil {
		return nil, err
	}
	p.IsActive = isActive == 1
	p.IsArchived = isArchived == 1
	p.ShowOnGlobalRoadmap = showGlobal == 1
	p.CreatedAt = time.Unix(createdAt, 0)
	return &p, nil
}

// listProjectsWithArchive returns projects ordered by creation date (desc) with
// feedback counts. When archived is non-nil, results are filtered to that archived
// flag. ListProjects and ListProjectsByArchive delegate here so the query/scan
// logic is not duplicated.
func (d *Database) listProjectsWithArchive(archived *bool) ([]Project, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `SELECT id, name, slug, description, is_active, is_archived, form_schema, announcement, show_on_global_roadmap, created_at FROM projects`
	args := []interface{}{}
	if archived != nil {
		v := 0
		if *archived {
			v = 1
		}
		query += ` WHERE is_archived = ?`
		args = append(args, v)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		var createdAt int64
		var isActive, isArchived, showGlobal int
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &isActive, &isArchived, &p.FormSchema, &p.Announcement, &showGlobal, &createdAt); err != nil {
			return nil, err
		}
		p.IsActive = isActive == 1
		p.IsArchived = isArchived == 1
		p.ShowOnGlobalRoadmap = showGlobal == 1
		p.CreatedAt = time.Unix(createdAt, 0)
		projects = append(projects, p)
	}

	// Batch feedback counts in a single query (parity with the original ListProjects).
	if len(projects) > 0 {
		countRows, err := d.db.Query(`SELECT project_id, COUNT(*) FROM feedbacks GROUP BY project_id`)
		if err == nil {
			defer countRows.Close()
			counts := make(map[string]int)
			for countRows.Next() {
				var pid string
				var cnt int
				if err := countRows.Scan(&pid, &cnt); err == nil {
					counts[pid] = cnt
				}
			}
			for i := range projects {
				projects[i].FeedbackCount = counts[projects[i].Slug]
			}
		}
	}

	return projects, nil
}

// ListProjects returns all projects ordered by creation date, with feedback counts.
func (d *Database) ListProjects() ([]Project, error) {
	return d.listProjectsWithArchive(nil)
}

// GetProjects returns distinct project IDs.
func (d *Database) GetProjects() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT DISTINCT project_id FROM feedbacks ORDER BY project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, nil
}

// GetProjectNameMap returns a slug -> display name map for all projects.
// Used by the admin feedback list to show human-readable names instead of
// raw slugs in the UI.
func (d *Database) GetProjectNameMap() (map[string]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT slug, name FROM projects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := make(map[string]string)
	for rows.Next() {
		var slug, name string
		if err := rows.Scan(&slug, &name); err != nil {
			return nil, err
		}
		m[slug] = name
	}
	return m, nil
}

// IsProjectActive checks if a project slug exists and is active.
// Returns false if the project doesn't exist (security fix: prevent spam to non-existent projects).
func (d *Database) IsProjectActive(slug string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var isActive int
	err := d.db.QueryRow(`SELECT is_active FROM projects WHERE slug = ?`, slug).Scan(&isActive)
	if err != nil {
		// Project not found — deny submission
		return false
	}
	return isActive == 1
}

// GetProjectStats returns per-project feedback counts.
func (d *Database) GetProjectStats() ([]map[string]interface{}, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT f.project_id, COUNT(*) as cnt,
			   COALESCE(MAX(f.created_at), 0) as latest,
			   COALESCE(p.name, '') as project_name
		FROM feedbacks f
		LEFT JOIN projects p ON p.slug = f.project_id
		GROUP BY f.project_id
		ORDER BY cnt DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var projectID string
		var projectName sql.NullString
		var count int
		var latestAt int64
		if err := rows.Scan(&projectID, &count, &latestAt, &projectName); err != nil {
			return nil, err
		}
		name := ""
		if projectName.Valid {
			name = projectName.String
		}
		latest := ""
		if latestAt > 0 {
			latest = time.Unix(latestAt, 0).Format("2006-01-02 15:04:05")
		}
		result = append(result, map[string]interface{}{
			"project_id":   projectID,
			"project_name": name,
			"count":        count,
			"latest_at":    latest,
		})
	}
	return result, nil
}

// GetProjectStatsForProjects returns per-project stats scoped to a list of project slugs.
func (d *Database) GetProjectStatsForProjects(projectIDs []string) ([]map[string]interface{}, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if len(projectIDs) == 0 {
		return []map[string]interface{}{}, nil
	}

	placeholders := make([]string, len(projectIDs))
	args := make([]interface{}, len(projectIDs))
	for i, pid := range projectIDs {
		placeholders[i] = "?"
		args[i] = pid
	}
	inClause := "f.project_id IN (" + strings.Join(placeholders, ",") + ")"

	rows, err := d.db.Query(`
		SELECT f.project_id, COUNT(*) as cnt,
			   COALESCE(MAX(f.created_at), 0) as latest,
			   COALESCE(p.name, '') as project_name
		FROM feedbacks f
		LEFT JOIN projects p ON p.slug = f.project_id
		WHERE `+inClause+`
		GROUP BY f.project_id
		ORDER BY cnt DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var projectID string
		var projectName sql.NullString
		var count int
		var latestAt int64
		if err := rows.Scan(&projectID, &count, &latestAt, &projectName); err != nil {
			return nil, err
		}
		name := ""
		if projectName.Valid {
			name = projectName.String
		}
		latest := ""
		if latestAt > 0 {
			latest = time.Unix(latestAt, 0).Format("2006-01-02 15:04:05")
		}
		result = append(result, map[string]interface{}{
			"project_id":   projectID,
			"project_name": name,
			"count":        count,
			"latest_at":    latest,
		})
	}
	if result == nil {
		result = []map[string]interface{}{}
	}
	return result, nil
}

// ArchiveProject sets a project's is_archived flag.
func (d *Database) ArchiveProject(id int64, archived bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	v := 0
	if archived {
		v = 1
	}
	_, err := d.db.Exec(`UPDATE projects SET is_archived = ? WHERE id = ?`, v, id)
	return err
}

// ListProjectsByArchive returns projects filtered by archived status.
func (d *Database) ListProjectsByArchive(archived bool) ([]Project, error) {
	return d.listProjectsWithArchive(&archived)
}

// ========== Slug History (Redirect) ==========

// InsertSlugHistory records an old slug for redirect purposes.
// If the old_slug already exists in history, it updates the target.
func (d *Database) InsertSlugHistory(oldSlug, newSlug string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(
		`INSERT INTO slug_history (old_slug, project_slug) VALUES (?, ?)
		 ON CONFLICT(old_slug) DO UPDATE SET project_slug = excluded.project_slug`,
		oldSlug, newSlug,
	)
	return err
}

// ResolveSlug checks if a slug is a historical slug and returns the current slug.
// Returns the original slug if no redirect is found.
func (d *Database) ResolveSlug(slug string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var currentSlug string
	err := d.db.QueryRow(`SELECT project_slug FROM slug_history WHERE old_slug = ?`, slug).Scan(&currentSlug)
	if err != nil {
		return slug // no redirect found, return original
	}
	return currentSlug
}
