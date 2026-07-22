package database

import (
	"database/sql"
	"strings"
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
	ViewCount   int       `json:"view_count"`
	UsefulVotes int       `json:"useful_votes"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   int64     `json:"updated_at"`
}

// PublicFAQ is the minimal projection returned by the public search endpoint.
// It MUST NOT leak project_slug / is_active / embedding. Answer is server-
// rendered (Markdown → sanitized HTML); UsefulVotes is the "👍 有用" count.
type PublicFAQ struct {
	ID          int64  `json:"id"`
	Question    string `json:"question"`
	Answer      string `json:"answer"`
	UsefulVotes int    `json:"useful_votes"`
}

// ListFAQs returns every FAQ of a project, including inactive ones, ordered by
// sort_order then id. The project_slug is a hard constraint. View counts come
// from the view_count column; useful-vote counts are batch-loaded so the admin
// list can show both without N+1 queries.
func (d *Database) ListFAQs(projectSlug string) ([]FAQ, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`
		SELECT id, project_slug, question, answer, is_active, sort_order, view_count, created_at, updated_at
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
		if err := rows.Scan(&f.ID, &f.ProjectSlug, &f.Question, &f.Answer, &isActive, &f.SortOrder, &f.ViewCount, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		f.IsActive = isActive == 1
		f.CreatedAt = time.Unix(createdAt, 0)
		f.UpdatedAt = updatedAt
		list = append(list, f)
	}
	// Batch-load useful-vote counts (target_type='faq') for the returned rows.
	if len(list) > 0 {
		ph := make([]string, len(list))
		args := make([]interface{}, len(list))
		for i, f := range list {
			ph[i] = "?"
			args[i] = f.ID
		}
		vrows, verr := d.db.Query(`SELECT feedback_id, COUNT(*) FROM feedback_votes WHERE target_type = 'faq' AND vote_type = 'useful' AND feedback_id IN (`+strings.Join(ph, ",")+`) GROUP BY feedback_id`, args...)
		if verr == nil {
			defer vrows.Close()
			vmap := make(map[int64]int, len(list))
			for vrows.Next() {
				var vid int64
				var n int
				if vrows.Scan(&vid, &n) == nil {
					vmap[vid] = n
				}
			}
			for i := range list {
				if v, ok := vmap[list[i].ID]; ok {
					list[i].UsefulVotes = v
				}
			}
		}
	}
	return list, nil
}

// ExportFAQs returns every FAQ of a project for CSV export (question/answer/
// sort_order/is_active), ordered by sort_order then id. Vote/view counts are
// intentionally omitted — they are runtime aggregates, not source content.
func (d *Database) ExportFAQs(projectSlug string) ([]FAQ, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`
		SELECT id, project_slug, question, answer, is_active, sort_order
		FROM faqs WHERE project_slug = ?
		ORDER BY sort_order, id`, projectSlug)
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

// GetFAQByID returns a single FAQ by primary key, or sql.ErrNoRows if absent.
func (d *Database) GetFAQByID(id int64) (*FAQ, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var f FAQ
	var isActive, createdAt, updatedAt int64
	err := d.db.QueryRow(`
		SELECT id, project_slug, question, answer, is_active, sort_order, view_count, created_at, updated_at
		FROM faqs WHERE id = ?`, id).
		Scan(&f.ID, &f.ProjectSlug, &f.Question, &f.Answer, &isActive, &f.SortOrder, &f.ViewCount, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	f.IsActive = isActive == 1
	f.CreatedAt = time.Unix(createdAt, 0)
	f.UpdatedAt = updatedAt
	return &f, nil
}

// IncrementFAQViewCount adds one to a FAQ's view_count and returns the new total.
func (d *Database) IncrementFAQViewCount(id int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.db.Exec(`UPDATE faqs SET view_count = view_count + 1 WHERE id = ?`, id); err != nil {
		return 0, err
	}
	var n int
	if err := d.db.QueryRow(`SELECT view_count FROM faqs WHERE id = ?`, id).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountFAQVotes returns the number of "useful" votes for a FAQ (target_type='faq').
func (d *Database) CountFAQVotes(faqID int64) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM feedback_votes WHERE feedback_id = ? AND target_type = 'faq' AND vote_type = 'useful'`, faqID).Scan(&n)
	return n, err
}

// CountFAQVotesMap returns a map of faq id -> useful-vote count for the given
// ids, so the public search can attach counts without N+1 queries.
func (d *Database) CountFAQVotesMap(ids []int64) (map[int64]int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make(map[int64]int, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	rows, err := d.db.Query(`SELECT feedback_id, COUNT(*) FROM feedback_votes WHERE target_type = 'faq' AND vote_type = 'useful' AND feedback_id IN (`+strings.Join(ph, ",")+`) GROUP BY feedback_id`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			continue
		}
		out[id] = n
	}
	return out, nil
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
