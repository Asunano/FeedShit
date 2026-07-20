package database

import (
	"strings"
)

func (d *Database) ListCategories(projectSlug string) ([]Category, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT id, project_slug, key, name, color, sort_order, is_active FROM categories WHERE project_slug = ? ORDER BY sort_order, id`, projectSlug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Category
	for rows.Next() {
		var c Category
		var isActive int
		if err := rows.Scan(&c.ID, &c.ProjectSlug, &c.Key, &c.Name, &c.Color, &c.SortOrder, &isActive); err != nil {
			return nil, err
		}
		c.IsActive = isActive == 1
		list = append(list, c)
	}
	return list, nil
}

func (d *Database) CreateCategory(projectSlug, key, name, color string, sortOrder int) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.db.Exec(`INSERT INTO categories (project_slug, key, name, color, sort_order) VALUES (?, ?, ?, ?, ?)`, projectSlug, key, name, color, sortOrder)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *Database) UpdateCategory(id int64, name, color string, sortOrder int, isActive bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	active := 0
	if isActive {
		active = 1
	}
	_, err := d.db.Exec(`UPDATE categories SET name = ?, color = ?, sort_order = ?, is_active = ? WHERE id = ?`, name, color, sortOrder, active, id)
	return err
}

func (d *Database) DeleteCategory(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Look up the category to get its project_slug and key
	var projectSlug, key string
	err := d.db.QueryRow(`SELECT project_slug, key FROM categories WHERE id = ?`, id).Scan(&projectSlug, &key)
	if err != nil {
		return err // not found or DB error
	}

	// Soft-delete: mark inactive instead of removing, so historical references
	// (including feedbacks that still reference this category) stay valid.
	// We deliberately do NOT clear feedbacks.category — that would destroy
	// historical data and contradict the soft-delete intent. Orphan cleanup
	// only applies to hard deletion, which is intentionally not supported here.
	_, err = d.db.Exec(`UPDATE categories SET is_active = 0 WHERE id = ?`, id)
	return err
}

// GetCategory returns a single category by ID. Used for RBAC ownership verification.
func (d *Database) GetCategory(id int64) (*Category, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var c Category
	var isActive int
	err := d.db.QueryRow(`SELECT id, project_slug, key, name, color, sort_order, is_active FROM categories WHERE id = ?`, id).
		Scan(&c.ID, &c.ProjectSlug, &c.Key, &c.Name, &c.Color, &c.SortOrder, &isActive)
	if err != nil {
		return nil, err
	}
	c.IsActive = isActive == 1
	return &c, nil
}

func (d *Database) GetCategoryByKey(projectSlug, key string) (*Category, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var c Category
	var isActive int
	err := d.db.QueryRow(`SELECT id, project_slug, key, name, color, sort_order, is_active FROM categories WHERE project_slug = ? AND key = ?`, projectSlug, key).Scan(&c.ID, &c.ProjectSlug, &c.Key, &c.Name, &c.Color, &c.SortOrder, &isActive)
	if err != nil {
		return nil, err
	}
	c.IsActive = isActive == 1
	return &c, nil
}

// GetCategoryCounts returns feedback counts grouped by category for a project (or all projects if projectSlug is empty).
func (d *Database) GetCategoryCounts(projectSlug string) (map[string]int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	where := ""
	args := []interface{}{}
	if projectSlug != "" {
		where = " WHERE project_id = ?"
		args = append(args, projectSlug)
	}
	rows, err := d.db.Query(`SELECT category, COUNT(*) FROM feedbacks`+where+` GROUP BY category`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int)
	for rows.Next() {
		var cat string
		var cnt int
		if err := rows.Scan(&cat, &cnt); err != nil {
			return nil, err
		}
		result[cat] = cnt
	}
	return result, nil
}

func (d *Database) UpdateFeedbackCategory(id int64, category string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE feedbacks SET category = ?, updated_at = strftime('%s', 'now') WHERE id = ?`, category, id)
	return err
}

func (d *Database) BulkUpdateCategory(ids []int64, category string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(ids) == 0 {
		return 0, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, category)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	res, err := d.db.Exec(`UPDATE feedbacks SET category = ?, updated_at = strftime('%s', 'now') WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
