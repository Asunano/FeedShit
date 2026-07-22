package database

import (
	"database/sql"
	"time"
)

// CreateAdmin inserts a new admin account. Returns the new ID.
func (d *Database) CreateAdmin(username, passwordHash, role string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	res, err := d.db.Exec(
		`INSERT INTO admins (username, password_hash, role, email) VALUES (?, ?, ?, '')`,
		username, passwordHash, role,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetAdminEmail updates the email address for an admin account.
func (d *Database) SetAdminEmail(adminID int64, email string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE admins SET email = ? WHERE id = ?`, email, adminID)
	return err
}

// GetAdminByUsername looks up an admin by username. Returns nil if not found.
func (d *Database) GetAdminByUsername(username string) (*Admin, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var a Admin
	var createdAt int64
	var isActive int
	var lastLoginAt int64
	err := d.db.QueryRow(
		`SELECT id, username, password_hash, role, is_active, last_login_at, created_at FROM admins WHERE username = ?`, username,
	).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &isActive, &lastLoginAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.IsActive = isActive == 1
	a.LastLoginAt = lastLoginAt
	a.CreatedAt = time.Unix(createdAt, 0)
	return &a, nil
}

// GetAdminByID looks up an admin by ID. Returns nil if not found.
func (d *Database) GetAdminByID(id int64) (*Admin, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var a Admin
	var createdAt int64
	var isActive int
	var lastLoginAt int64
	err := d.db.QueryRow(
		`SELECT id, username, password_hash, role, is_active, last_login_at, created_at FROM admins WHERE id = ?`, id,
	).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &isActive, &lastLoginAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.IsActive = isActive == 1
	a.LastLoginAt = lastLoginAt
	a.CreatedAt = time.Unix(createdAt, 0)
	return &a, nil
}

// ListAdmins returns all admin accounts.
func (d *Database) ListAdmins() ([]Admin, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT id, username, password_hash, role, is_active, last_login_at, created_at FROM admins ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Admin
	for rows.Next() {
		var a Admin
		var createdAt int64
		var isActive int
		var lastLoginAt int64
		if err := rows.Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &isActive, &lastLoginAt, &createdAt); err != nil {
			return nil, err
		}
		a.IsActive = isActive == 1
		a.LastLoginAt = lastLoginAt
		a.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, a)
	}
	return list, nil
}

// UpdateAdmin updates an admin's role and/or active status. If passwordHash is non-empty, also updates password.
func (d *Database) UpdateAdmin(id int64, role string, isActive bool, passwordHash string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	active := 0
	if isActive {
		active = 1
	}
	if passwordHash != "" {
		_, err := d.db.Exec(`UPDATE admins SET role = ?, is_active = ?, password_hash = ? WHERE id = ?`, role, active, passwordHash, id)
		return err
	}
	_, err := d.db.Exec(`UPDATE admins SET role = ?, is_active = ? WHERE id = ?`, role, active, id)
	return err
}

// DeleteAdmin removes an admin account by ID.
func (d *Database) DeleteAdmin(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`DELETE FROM admins WHERE id = ?`, id)
	return err
}

// UpdateAdminPassword updates the password hash for an admin.
func (d *Database) UpdateAdminPassword(id int64, passwordHash string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE admins SET password_hash = ? WHERE id = ?`, passwordHash, id)
	return err
}

// UpdateAdminLastLogin records the timestamp of an admin's most recent login.
func (d *Database) UpdateAdminLastLogin(adminID, loginAt int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE admins SET last_login_at = ? WHERE id = ?`, loginAt, adminID)
	return err
}

// GetAdminEmail returns the admin's email from their account record.
// Previously used a heuristic based on feedback contact_name �? now authoritative.
func (d *Database) GetAdminEmail(username string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var email string
	d.db.QueryRow(`SELECT email FROM admins WHERE username = ? AND email != ''`, username).Scan(&email)
	return email
}

// CountAdmins returns the total number of admin accounts.
func (d *Database) CountAdmins() (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var count int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&count)
	return count, err
}

// ========== Member Grants (Fine-grained RBAC) ==========

// ListMemberGrants returns all grants for a specific admin.
func (d *Database) ListMemberGrants(adminID int64) ([]MemberGrant, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT id, admin_id, project_slug, category_key, role FROM member_grants WHERE admin_id = ? ORDER BY project_slug, category_key`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []MemberGrant
	for rows.Next() {
		var g MemberGrant
		if err := rows.Scan(&g.ID, &g.AdminID, &g.ProjectSlug, &g.CategoryKey, &g.Role); err != nil {
			return nil, err
		}
		list = append(list, g)
	}
	return list, nil
}

// SetMemberGrants replaces all grants for an admin with the given list.
func (d *Database) SetMemberGrants(adminID int64, grants []MemberGrant) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM member_grants WHERE admin_id = ?`, adminID); err != nil {
		return err
	}
	for _, g := range grants {
		if _, err := tx.Exec(`INSERT INTO member_grants (admin_id, project_slug, category_key, role) VALUES (?, ?, ?, ?)`,
			adminID, g.ProjectSlug, g.CategoryKey, g.Role); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteMemberGrant removes a single grant by ID.
func (d *Database) DeleteMemberGrant(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM member_grants WHERE id = ?`, id)
	return err
}

// GetAllowedProjectSlugs returns distinct project slugs from member_grants for an admin.
// Returns nil if the admin has no grants (meaning no restriction �? can see all).
func (d *Database) GetAllowedProjectSlugs(adminID int64) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT DISTINCT project_slug FROM member_grants WHERE admin_id = ?`, adminID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var slugs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil
		}
		slugs = append(slugs, s)
	}
	if len(slugs) == 0 {
		return nil
	}
	return slugs
}

// GetEffectiveRole returns the effective role for an admin on a (project, category) pair.
// Priority: exact (project, category) > (project, '*') > empty (no grant).
func (d *Database) GetEffectiveRole(adminID int64, projectSlug, categoryKey string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	// Refer to middleware.RoleLevel for the shared definition
	roleLevel := map[string]int{"viewer": 1, "editor": 2, "manager": 3, "admin": 4}
	exactRole := ""
	exactLevel := 0
	wildcardRole := ""
	wildcardLevel := 0
	rows, err := d.db.Query(`SELECT category_key, role FROM member_grants WHERE admin_id = ? AND project_slug = ?`, adminID, projectSlug)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var cat, role string
		if err := rows.Scan(&cat, &role); err != nil {
			continue
		}
		lvl := roleLevel[role]
		if cat == categoryKey && lvl > exactLevel {
			exactLevel = lvl
			exactRole = role
		} else if cat == "*" && lvl > wildcardLevel {
			wildcardLevel = lvl
			wildcardRole = role
		}
	}
	// Exact match takes precedence over wildcard
	if exactRole != "" {
		return exactRole
	}
	return wildcardRole
}

// GetAllowedCategories returns the category keys an admin is granted for a specific project.
// If '*' is present, returns nil (meaning all categories).
func (d *Database) GetAllowedCategories(adminID int64, projectSlug string) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT DISTINCT category_key FROM member_grants WHERE admin_id = ? AND project_slug = ?`, adminID, projectSlug)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var cats []string
	hasWildcard := false
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			continue
		}
		if k == "*" {
			hasWildcard = true
		}
		cats = append(cats, k)
	}
	if hasWildcard {
		return nil // nil means "all categories"
	}
	return cats
}

// ========== Member Grants (Access Isolation) ==========

// GetAdminProjectSlugs returns the list of project slugs an admin can access.
// Uses member_grants table for fine-grained RBAC.
// Returns empty slice if the admin has no grants (no access).
// Admin role always returns nil (unrestricted).
func (d *Database) GetAdminProjectSlugs(adminID int64, role string) ([]string, error) {
	if role == "admin" {
		return nil, nil // admins see everything
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT DISTINCT project_slug FROM member_grants WHERE admin_id = ? ORDER BY project_slug`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slugs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		slugs = append(slugs, s)
	}
	if slugs == nil {
		return []string{}, nil // no grants = no access
	}
	return slugs, nil
}

// GetAdminAccessPlan returns the per-project access plan for a non-admin user.
// Returns nil if the user has full access (no grants = unrestricted for backward compat).
func (d *Database) GetAdminAccessPlan(adminID int64) ([]ProjectAccess, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT project_slug, category_key FROM member_grants WHERE admin_id = ? ORDER BY project_slug, category_key`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projectCats := make(map[string][]string)
	var order []string
	for rows.Next() {
		var slug, cat string
		if err := rows.Scan(&slug, &cat); err != nil {
			return nil, err
		}
		if _, exists := projectCats[slug]; !exists {
			order = append(order, slug)
		}
		projectCats[slug] = append(projectCats[slug], cat)
	}
	if len(order) == 0 {
		return []ProjectAccess{}, nil // no grants = no access
	}
	plan := make([]ProjectAccess, 0, len(order))
	for _, slug := range order {
		cats := projectCats[slug]
		hasWildcard := false
		for _, c := range cats {
			if c == "*" {
				hasWildcard = true
				break
			}
		}
		if hasWildcard {
			plan = append(plan, ProjectAccess{Slug: slug, AllowedCategories: nil})
		} else {
			plan = append(plan, ProjectAccess{Slug: slug, AllowedCategories: cats})
		}
	}
	return plan, nil
}
