package database

import (
	"database/sql"
	"time"
)

// FAQ is the persistence model for a single knowledge-base entry of a project.
// The Embedding field is intentionally json:"-" so it is never serialized to clients.
type FAQ struct {
	ID          int64     `json:"id"`
	ProjectSlug string    `json:"project_slug"`
	Question    string    `json:"question"`
	Answer      string    `json:"answer"`
	Embedding   string    `json:"-"` // never exposed; always empty this phase (P2 vector upgrade)
	IsActive    bool      `json:"is_active"`
	SortOrder   int       `json:"sort_order"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   int64     `json:"updated_at"`
}

// PublicFAQ is the minimal projection returned by the public search endpoint.
// It MUST NOT leak project_slug / is_active / embedding.
type PublicFAQ struct {
	ID       int64  `json:"id"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// ListFAQs returns every FAQ of a project, including inactive ones, ordered by
// sort_order then id. The project_slug is a hard constraint.
func (d *Database) ListFAQs(projectSlug string) ([]FAQ, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`
		SELECT id, project_slug, question, answer, is_active, sort_order, created_at, updated_at
		FROM faqs
		WHERE project_slug = ?
		ORDER BY sort_order, id`, projectSlug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []FAQ
	for rows.Next() {
		var f FAQ
		var isActive, createdAt, updatedAt int64
		if err := rows.Scan(&f.ID, &f.ProjectSlug, &f.Question, &f.Answer, &isActive, &f.SortOrder, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		f.IsActive = isActive == 1
		f.CreatedAt = time.Unix(createdAt, 0)
		f.UpdatedAt = updatedAt
		list = append(list, f)
	}
	return list, nil
}

// GetFAQByQuestion returns the FAQ matching (project_slug, question) or
// sql.ErrNoRows when none exists. Used for duplicate detection on create.
func (d *Database) GetFAQByQuestion(projectSlug, question string) (*FAQ, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var f FAQ
	var isActive, createdAt, updatedAt int64
	err := d.db.QueryRow(`
		SELECT id, project_slug, question, answer, is_active, sort_order, created_at, updated_at
		FROM faqs
		WHERE project_slug = ? AND question = ?`, projectSlug, question).
		Scan(&f.ID, &f.ProjectSlug, &f.Question, &f.Answer, &isActive, &f.SortOrder, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	f.IsActive = isActive == 1
	f.CreatedAt = time.Unix(createdAt, 0)
	f.UpdatedAt = updatedAt
	return &f, nil
}

// CreateFAQ inserts a new FAQ owned by projectSlug. updated_at is set to now.
// Returns the new row id.
func (d *Database) CreateFAQ(projectSlug, question, answer string, sortOrder int, isActive bool) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	active := 0
	if isActive {
		active = 1
	}
	res, err := d.db.Exec(`
		INSERT INTO faqs (project_slug, question, answer, is_active, sort_order, updated_at)
		VALUES (?, ?, ?, ?, ?, strftime('%s', 'now'))`,
		projectSlug, question, answer, active, sortOrder)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateFAQ updates the FAQ identified by id, but ONLY when it belongs to
// projectSlug (project_slug hard constraint). Returns sql.ErrNoRows when no
// matching row exists so callers can surface a 404.
func (d *Database) UpdateFAQ(id int64, projectSlug, question, answer string, sortOrder int, isActive bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	active := 0
	if isActive {
		active = 1
	}
	res, err := d.db.Exec(`
		UPDATE faqs
		SET question = ?, answer = ?, sort_order = ?, is_active = ?, updated_at = strftime('%s', 'now')
		WHERE id = ? AND project_slug = ?`,
		question, answer, sortOrder, active, id, projectSlug)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteFAQ hard-deletes the FAQ identified by id, constrained to projectSlug.
// Returns sql.ErrNoRows when no matching row exists so callers can surface a 404.
func (d *Database) DeleteFAQ(id int64, projectSlug string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.db.Exec(`DELETE FROM faqs WHERE id = ? AND project_slug = ?`, id, projectSlug)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SearchFAQs returns active FAQs of projectSlug whose question or answer matches
// q (case-insensitive LIKE), ordered question-hit-first then sort_order then id,
// limited to `limit` rows. q MUST already be wrapped as "%q%" by the caller.
func (d *Database) SearchFAQs(projectSlug, q string, limit int) ([]FAQ, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`
		SELECT id, project_slug, question, answer, is_active, sort_order
		FROM faqs
		WHERE project_slug = ?
		  AND is_active = 1
		  AND (LOWER(question) LIKE LOWER(?) OR LOWER(answer) LIKE LOWER(?))
		ORDER BY
		  CASE WHEN LOWER(question) LIKE LOWER(?) THEN 0 ELSE 1 END,
		  sort_order ASC, id ASC
		LIMIT ?`,
		projectSlug, q, q, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []FAQ
	for rows.Next() {
		var f FAQ
		var isActive int
		if err := rows.Scan(&f.ID, &f.ProjectSlug, &f.Question, &f.Answer, &isActive, &f.SortOrder); err != nil {
			return nil, err
		}
		f.IsActive = isActive == 1
		list = append(list, f)
	}
	return list, nil
}
