package database

import (
	"database/sql"
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
	res, err := d.db.Exec(
		`INSERT INTO projects (name, slug, description, is_active, is_archived, form_schema) VALUES (?, ?, ?, ?, ?, ?)`,
		p.Name, p.Slug, p.Description, active, archived, p.FormSchema,
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
	_, err := d.db.Exec(
		`UPDATE projects SET name = ?, slug = ?, description = ?, is_active = ?, is_archived = ?, form_schema = ? WHERE id = ?`,
		p.Name, p.Slug, p.Description, active, archived, p.FormSchema, p.ID,
	)
	return err
}

// DeleteProject removes a project and all associated feedbacks (cascade).
func (d *Database) DeleteProject(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// First get the project slug to delete associated feedbacks
	var slug string
	err := d.db.QueryRow(`SELECT slug FROM projects WHERE id = ?`, id).Scan(&slug)
	if err != nil {
		return err
	}

	// Delete associated feedbacks
	if _, err := d.db.Exec(`DELETE FROM feedbacks WHERE project_id = ?`, slug); err != nil {
		return err
	}

	// Delete the project
	_, err = d.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

// GetProject returns a project by ID.
func (d *Database) GetProject(id int64) (*Project, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var p Project
	var createdAt int64
	var isActive, isArchived int
	err := d.db.QueryRow(
		`SELECT id, name, slug, description, is_active, is_archived, form_schema, created_at FROM projects WHERE id = ?`, id,
	).Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &isActive, &isArchived, &p.FormSchema, &createdAt)
	if err != nil {
		return nil, err
	}
	p.IsActive = isActive == 1
	p.IsArchived = isArchived == 1
	p.CreatedAt = time.Unix(createdAt, 0)
	return &p, nil
}

// GetProjectBySlug returns a project by its slug.
func (d *Database) GetProjectBySlug(slug string) (*Project, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var p Project
	var createdAt int64
	var isActive, isArchived int
	err := d.db.QueryRow(
		`SELECT id, name, slug, description, is_active, is_archived, form_schema, created_at FROM projects WHERE slug = ?`, slug,
	).Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &isActive, &isArchived, &p.FormSchema, &createdAt)
	if err != nil {
		return nil, err
	}
	p.IsActive = isActive == 1
	p.IsArchived = isArchived == 1
	p.CreatedAt = time.Unix(createdAt, 0)
	return &p, nil
}

// ListProjects returns all projects ordered by creation date, with feedback counts.
func (d *Database) ListProjects() ([]Project, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT id, name, slug, description, is_active, is_archived, form_schema, created_at FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		var createdAt int64
		var isActive, isArchived int
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &isActive, &isArchived, &p.FormSchema, &createdAt); err != nil {
			return nil, err
		}
		p.IsActive = isActive == 1
		p.IsArchived = isArchived == 1
		p.CreatedAt = time.Unix(createdAt, 0)
		projects = append(projects, p)
	}

	// Batch feedback counts in a single query
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
	d.mu.RLock()
	defer d.mu.RUnlock()

	v := 0
	if archived {
		v = 1
	}
	rows, err := d.db.Query(`SELECT id, name, slug, description, is_active, is_archived, form_schema, created_at FROM projects WHERE is_archived = ? ORDER BY created_at DESC`, v)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		var createdAt int64
		var isActive, isArchived int
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &isActive, &isArchived, &p.FormSchema, &createdAt); err != nil {
			return nil, err
		}
		p.IsActive = isActive == 1
		p.IsArchived = isArchived == 1
		p.CreatedAt = time.Unix(createdAt, 0)
		projects = append(projects, p)
	}
	return projects, nil
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
