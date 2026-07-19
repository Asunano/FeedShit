package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
	"feedshit/internal/config"
)

// Feedback represents a single feedback submission.
type Feedback struct {
	ID            int64     `json:"id"`
	ProjectID     string    `json:"project_id"`
	Title         string    `json:"title"`
	Description   string    `json:"description"`
	CustomData    string    `json:"custom_data"`
	FilePaths     string    `json:"file_paths"`
	ClientIP      string    `json:"client_ip"`
	Status        string    `json:"status"`
	Tags          string    `json:"tags"`
	Assignee      string    `json:"assignee"`
	ContactName   string    `json:"contact_name"`
	ContactEmail  string    `json:"contact_email"`
	TrackingToken string    `json:"tracking_token,omitempty"`
	Priority      string    `json:"priority"`
	IsDuplicate   bool      `json:"is_duplicate"`
	DuplicateOf   int64     `json:"duplicate_of"`
	Category      string    `json:"category"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     int64     `json:"updated_at"`
}

// Admin represents a team member with login credentials.
type Admin struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
}

// FeedbackNote represents an admin reply or internal note on a feedback.
type FeedbackNote struct {
	ID         int64     `json:"id"`
	FeedbackID int64     `json:"feedback_id"`
	Content    string    `json:"content"`
	Author     string    `json:"author"`
	IsPublic   bool      `json:"is_public"`
	CreatedAt  time.Time `json:"created_at"`
}

// Project represents a feedback collection project.
type Project struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`
	Description   string    `json:"description"`
	IsActive      bool      `json:"is_active"`
	IsArchived    bool      `json:"is_archived"`
	FormSchema    string    `json:"form_schema"`
	FeedbackCount int       `json:"feedback_count"`
	CreatedAt     time.Time `json:"created_at"`
}

// Category represents a feedback classification within a project.
type Category struct {
	ID          int64  `json:"id"`
	ProjectSlug string `json:"project_slug"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	SortOrder   int    `json:"sort_order"`
	IsActive    bool   `json:"is_active"`
}

// MemberGrant represents a fine-grained permission: (admin × project × category → role).
type MemberGrant struct {
	ID          int64  `json:"id"`
	AdminID     int64  `json:"admin_id"`
	ProjectSlug string `json:"project_slug"`
	CategoryKey string `json:"category_key"`
	Role        string `json:"role"`
}

// DBConfig represents a configuration entry stored in SQLite.
type DBConfig struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

// AuditLog represents an admin action audit record.
type AuditLog struct {
	ID        int64     `json:"id"`
	Action    string    `json:"action"`
	Detail    string    `json:"detail"`
	User      string    `json:"user"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

// Database wraps the sql.DB connection and provides application-specific operations.
type Database struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewDatabase opens (or creates) the SQLite database and initializes schema.
func NewDatabase(dbPath string) (*Database, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Single-connection mode: consistent with the manual RWMutex in Database struct.
	// WAL is kept for crash recovery, but concurrent reads are not utilized.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	d := &Database{db: db}
	if err := d.initDB(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init db: %w", err)
	}
	return d, nil
}

// NewTestDatabase creates an in-memory SQLite database for testing.
func NewTestDatabase() (*Database, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	d := &Database{db: db}
	if err := d.initDB(); err != nil {
		db.Close()
		return nil, err
	}
	return d, nil
}

func (d *Database) initDB() error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-8000",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := d.db.Exec(p); err != nil {
			log.Printf("WARN: pragma %q failed: %v", p, err)
		}
	}
	return d.migrate()
}

func (d *Database) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS feedbacks (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id  TEXT    NOT NULL,
		title       TEXT    NOT NULL,
		description TEXT    NOT NULL DEFAULT '',
		custom_data TEXT    NOT NULL DEFAULT '{}',
		file_paths  TEXT    NOT NULL DEFAULT '[]',
		client_ip   TEXT    NOT NULL DEFAULT '',
		status      TEXT    NOT NULL DEFAULT 'pending',
		tags        TEXT    NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
		updated_at  INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_feedbacks_project ON feedbacks(project_id);
	CREATE INDEX IF NOT EXISTS idx_feedbacks_created ON feedbacks(created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_feedbacks_status ON feedbacks(status);

	CREATE TABLE IF NOT EXISTS config (
		key         TEXT PRIMARY KEY,
		value       TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE IF NOT EXISTS projects (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT    NOT NULL,
		slug        TEXT    NOT NULL UNIQUE,
		description TEXT    NOT NULL DEFAULT '',
		is_active   INTEGER NOT NULL DEFAULT 1,
		form_schema TEXT    NOT NULL DEFAULT '[]',
		created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
	);
	CREATE INDEX IF NOT EXISTS idx_projects_slug ON projects(slug);

	CREATE TABLE IF NOT EXISTS audit_logs (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		action     TEXT    NOT NULL,
		detail     TEXT    NOT NULL DEFAULT '',
		user       TEXT    NOT NULL DEFAULT '',
		ip         TEXT    NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
	);
	CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at DESC);
	`
	if _, err := d.db.Exec(schema); err != nil {
		return err
	}
	// Add columns for existing databases (ignore "duplicate column" errors)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN assignee TEXT NOT NULL DEFAULT ''`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN contact_name TEXT NOT NULL DEFAULT ''`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN contact_email TEXT NOT NULL DEFAULT ''`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN tracking_token TEXT NOT NULL DEFAULT ''`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN priority TEXT NOT NULL DEFAULT ''`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN is_duplicate INTEGER NOT NULL DEFAULT 0`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN duplicate_of INTEGER NOT NULL DEFAULT 0`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN category TEXT NOT NULL DEFAULT ''`)
	d.db.Exec(`CREATE INDEX IF NOT EXISTS idx_feedbacks_category ON feedbacks(project_id, category)`)

	// Indexes for frequently queried columns
	d.db.Exec(`CREATE INDEX IF NOT EXISTS idx_feedbacks_token ON feedbacks(tracking_token)`)
	d.db.Exec(`CREATE INDEX IF NOT EXISTS idx_feedbacks_assignee ON feedbacks(assignee)`)
	d.db.Exec(`CREATE INDEX IF NOT EXISTS idx_feedbacks_priority ON feedbacks(priority)`)

	// Feedback notes table (admin replies / internal notes)
	d.db.Exec(`
		CREATE TABLE IF NOT EXISTS feedback_notes (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			feedback_id INTEGER NOT NULL,
			content     TEXT    NOT NULL,
			author      TEXT    NOT NULL DEFAULT '',
			is_public   INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		);
		CREATE INDEX IF NOT EXISTS idx_notes_feedback ON feedback_notes(feedback_id);
	`)

	// Admins table for multi-admin team support
	d.db.Exec(`
		CREATE TABLE IF NOT EXISTS admins (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			username      TEXT    NOT NULL UNIQUE,
			password_hash TEXT    NOT NULL,
			role          TEXT    NOT NULL DEFAULT 'editor',
			is_active     INTEGER NOT NULL DEFAULT 1,
			created_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		);
	`)

	// API tokens for external system integration
	d.db.Exec(`
		CREATE TABLE IF NOT EXISTS api_tokens (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			token         TEXT    NOT NULL UNIQUE,
			name          TEXT    NOT NULL DEFAULT '',
			project_id    TEXT    NOT NULL DEFAULT '',
			is_active     INTEGER NOT NULL DEFAULT 1,
			last_used_at  TEXT    NOT NULL DEFAULT '',
			created_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		);
	`)

	// Add is_archived column to projects (idempotent migration)
	d.db.Exec(`ALTER TABLE projects ADD COLUMN is_archived INTEGER NOT NULL DEFAULT 0`)

	// Fine-grained member grants: (admin × project × category → role)
	d.db.Exec(`
		CREATE TABLE IF NOT EXISTS member_grants (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			admin_id      INTEGER NOT NULL,
			project_slug  TEXT    NOT NULL,
			category_key  TEXT    NOT NULL DEFAULT '*',
			role          TEXT    NOT NULL DEFAULT 'viewer',
			UNIQUE(admin_id, project_slug, category_key)
		);
		CREATE INDEX IF NOT EXISTS idx_grants_admin ON member_grants(admin_id);
	`)

	// Clean up legacy project_members table if it exists (data already migrated to member_grants)
	d.db.Exec(`DROP TABLE IF EXISTS project_members`)

	// Slug history: redirect old slugs after rename
	d.db.Exec(`
		CREATE TABLE IF NOT EXISTS slug_history (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			old_slug    TEXT    NOT NULL UNIQUE,
			project_slug TEXT   NOT NULL
		);
	`)

	// Categories: feedback classification per project
	d.db.Exec(`
		CREATE TABLE IF NOT EXISTS categories (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			project_slug TEXT    NOT NULL,
			key          TEXT    NOT NULL,
			name         TEXT    NOT NULL,
			color        TEXT    NOT NULL DEFAULT '',
			sort_order   INTEGER NOT NULL DEFAULT 0,
			is_active    INTEGER NOT NULL DEFAULT 1,
			UNIQUE(project_slug, key)
		);
		CREATE INDEX IF NOT EXISTS idx_categories_project ON categories(project_slug);
	`)

	return nil
}

// InsertFeedback inserts a new feedback record and returns its ID.
func (d *Database) InsertFeedback(f *Feedback) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	status := f.Status
	if status == "" {
		status = "pending"
	}
	res, err := d.db.Exec(
		`INSERT INTO feedbacks (project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, category, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'))`,
		f.ProjectID, f.Title, f.Description, f.CustomData, f.FilePaths, f.ClientIP, status, f.Tags, f.Assignee, f.ContactName, f.ContactEmail, f.TrackingToken, f.Priority, f.Category,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	f.ID = id
	f.CreatedAt = time.Now()
	return id, nil
}

// buildAccessPlanWhere constructs a SQL WHERE clause from a ProjectAccess slice.
// Projects with nil AllowedCategories (wildcard '*') use simple project_id filter.
// Projects with specific categories use (project_id = ? AND category IN (...)).
// All conditions are OR'd together.
func buildAccessPlanWhere(plan []ProjectAccess) (string, []interface{}) {
	if len(plan) == 0 {
		return " WHERE 1=0", nil // no access
	}

	var orConditions []string
	args := []interface{}{}
	var unrestrictedSlugs []string

	for _, pa := range plan {
		if pa.AllowedCategories == nil {
			unrestrictedSlugs = append(unrestrictedSlugs, pa.Slug)
		} else if len(pa.AllowedCategories) > 0 {
			placeholders := make([]string, len(pa.AllowedCategories))
			for i, cat := range pa.AllowedCategories {
				placeholders[i] = "?"
				args = append(args, cat)
			}
			orConditions = append(orConditions, "(category IN ("+strings.Join(placeholders, ",")+") AND project_id = ?)")
			args = append(args, pa.Slug)
		}
	}

	// Build unrestricted slugs condition
	if len(unrestrictedSlugs) == 1 {
		orConditions = append([]string{"project_id = ?"}, orConditions...)
		args = append([]interface{}{unrestrictedSlugs[0]}, args...)
	} else if len(unrestrictedSlugs) > 1 {
		placeholders := make([]string, len(unrestrictedSlugs))
		slugArgs := make([]interface{}, len(unrestrictedSlugs))
		for i, s := range unrestrictedSlugs {
			placeholders[i] = "?"
			slugArgs[i] = s
		}
		orConditions = append([]string{"project_id IN (" + strings.Join(placeholders, ",") + ")"}, orConditions...)
		args = append(slugArgs, args...)
	}

	if len(orConditions) == 0 {
		return " WHERE 1=0", nil
	}
	if len(orConditions) == 1 {
		return " WHERE " + orConditions[0], args
	}
	return " WHERE (" + strings.Join(orConditions, " OR ") + ")", args
}

// ListFeedbacks returns feedbacks filtered by project_id (empty = all), paginated.
func (d *Database) ListFeedbacks(projectIDs []string, accessPlan []ProjectAccess, limit, offset int) ([]Feedback, int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var total int
	var rows *sql.Rows
	var err error

	const cols = `id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, is_duplicate, duplicate_of, category, created_at, updated_at`

	where := ""
	args := []interface{}{}

	if accessPlan != nil {
		where, args = buildAccessPlanWhere(accessPlan)
	} else if len(projectIDs) == 1 {
		where = ` WHERE project_id = ?`
		args = append(args, projectIDs[0])
	} else if len(projectIDs) > 1 {
		placeholders := make([]string, len(projectIDs))
		for i, pid := range projectIDs {
			placeholders[i] = "?"
			args = append(args, pid)
		}
		where = ` WHERE project_id IN (` + strings.Join(placeholders, ",") + `)`
	}

	err = d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks`+where, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	queryArgs := append(args, limit, offset)
	rows, err = d.db.Query(
		`SELECT `+cols+` FROM feedbacks`+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		queryArgs...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []Feedback
	for rows.Next() {
		var f Feedback
		var createdAt int64
		var isDuplicate int
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &f.TrackingToken, &f.Priority, &isDuplicate, &f.DuplicateOf, &f.Category, &createdAt, &f.UpdatedAt); err != nil {
			return nil, 0, err
		}
		f.IsDuplicate = isDuplicate == 1
		f.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, f)
	}
	return list, total, nil
}

// SearchFeedbacks supports keyword search across multiple fields, status/priority/assignee filters, and project filter.
func (d *Database) SearchFeedbacks(projectIDs []string, accessPlan []ProjectAccess, keyword, status, priority, assignee, category string, limit, offset int) ([]Feedback, int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	where := "WHERE 1=1"
	args := []interface{}{}

	if accessPlan != nil {
		planWhere, planArgs := buildAccessPlanWhere(accessPlan)
		// Replace "WHERE 1=1" with the access plan WHERE clause
		if planWhere != "" {
			// planWhere is " WHERE (...)" — append as AND condition
			where += " AND" + strings.TrimPrefix(planWhere, " WHERE")
			args = append(args, planArgs...)
		}
	} else if len(projectIDs) == 1 {
		where += " AND project_id = ?"
		args = append(args, projectIDs[0])
	} else if len(projectIDs) > 1 {
		placeholders := make([]string, len(projectIDs))
		for i, pid := range projectIDs {
			placeholders[i] = "?"
			args = append(args, pid)
		}
		where += " AND project_id IN (" + strings.Join(placeholders, ",") + ")"
	}
	if status != "" {
		where += " AND status = ?"
		args = append(args, status)
	}
	if priority != "" {
		where += " AND priority = ?"
		args = append(args, priority)
	}
	if assignee != "" {
		where += " AND assignee = ?"
		args = append(args, assignee)
	}
	if category != "" {
		where += " AND category = ?"
		args = append(args, category)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		where += ` AND (title LIKE ? OR description LIKE ? OR tags LIKE ? OR contact_name LIKE ? OR contact_email LIKE ? OR id IN (SELECT feedback_id FROM feedback_notes WHERE content LIKE ?))`
		args = append(args, like, like, like, like, like, like)
	}

	const cols = `id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, is_duplicate, duplicate_of, category, created_at, updated_at`

	var total int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks `+where, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	queryArgs := append(args, limit, offset)
	rows, err := d.db.Query(
		`SELECT `+cols+` FROM feedbacks `+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		queryArgs...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []Feedback
	for rows.Next() {
		var f Feedback
		var createdAt int64
		var isDuplicate int
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &f.TrackingToken, &f.Priority, &isDuplicate, &f.DuplicateOf, &f.Category, &createdAt, &f.UpdatedAt); err != nil {
			return nil, 0, err
		}
		f.IsDuplicate = isDuplicate == 1
		f.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, f)
	}
	return list, total, nil
}

// GetFeedback returns a single feedback by ID.
func (d *Database) GetFeedback(id int64) (*Feedback, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var f Feedback
	var createdAt int64
	var isDuplicate int
	err := d.db.QueryRow(
		`SELECT id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, is_duplicate, duplicate_of, category, created_at, updated_at
		 FROM feedbacks WHERE id = ?`, id,
	).Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &f.TrackingToken, &f.Priority, &isDuplicate, &f.DuplicateOf, &f.Category, &createdAt, &f.UpdatedAt)
	if err != nil {
		return nil, err
	}
	f.IsDuplicate = isDuplicate == 1
	f.CreatedAt = time.Unix(createdAt, 0)
	return &f, nil
}

// UpdateFeedbackStatus updates the status and/or tags of a feedback.
func (d *Database) UpdateFeedbackStatus(id int64, status, tags string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`UPDATE feedbacks SET status = ?, tags = ?, updated_at = strftime('%s', 'now') WHERE id = ?`, status, tags, id)
	return err
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

// GetStats returns dashboard statistics.
func (d *Database) GetStats() (total int, projects int, today int, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	err = d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks`).Scan(&total)
	if err != nil {
		return
	}
	err = d.db.QueryRow(`SELECT COUNT(DISTINCT project_id) FROM feedbacks`).Scan(&projects)
	if err != nil {
		return
	}
	err = d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks WHERE date(created_at, 'unixepoch') = date('now')`).Scan(&today)
	return
}

// GetConfig retrieves a config value by key. Returns empty string if not found.
func (d *Database) GetConfig(key string) string {
	var value string
	err := d.db.QueryRow(`SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return ""
	}
	return value
}

// SetConfig upserts a config entry.
func (d *Database) SetConfig(key, value, description string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(
		`INSERT INTO config (key, value, description) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, description = excluded.description`,
		key, value, description,
	)
	return err
}

// GetAllConfig returns all config entries.
func (d *Database) GetAllConfig() ([]DBConfig, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT key, value, description FROM config ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []DBConfig
	for rows.Next() {
		var c DBConfig
		if err := rows.Scan(&c.Key, &c.Value, &c.Description); err != nil {
			return nil, err
		}
		configs = append(configs, c)
	}
	return configs, nil
}

// InitDefaultConfig seeds default config values if they don't exist.
func (d *Database) InitDefaultConfig(cfg *config.Config) {
	defaults := []DBConfig{
		{Key: "smtp_host", Value: cfg.SMTPHost, Description: "SMTP 服务器地址"},
		{Key: "smtp_port", Value: fmt.Sprintf("%d", cfg.SMTPPort), Description: "SMTP 端口"},
		{Key: "smtp_user", Value: cfg.SMTPUser, Description: "SMTP 用户名"},
		{Key: "smtp_pass", Value: cfg.SMTPPass, Description: "SMTP 密码"},
		{Key: "smtp_from", Value: cfg.SMTPFrom, Description: "发件人地址"},
		{Key: "smtp_to", Value: cfg.SMTPTo, Description: "收件人地址（多个用逗号分隔）"},
		{Key: "notify_enable", Value: fmt.Sprintf("%t", cfg.NotifyEnable), Description: "是否启用邮件通知"},
	}
	for _, item := range defaults {
		var count int
		d.db.QueryRow(`SELECT COUNT(*) FROM config WHERE key = ?`, item.Key).Scan(&count)
		if count == 0 {
			if err := d.SetConfig(item.Key, item.Value, item.Description); err != nil {
				log.Printf("WARN: failed to seed config %q: %v", item.Key, err)
			}
		}
	}
}

// DeleteFeedback removes a feedback record by ID.
func (d *Database) DeleteFeedback(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Cascade delete notes first
	d.db.Exec(`DELETE FROM feedback_notes WHERE feedback_id = ?`, id)
	_, err := d.db.Exec(`DELETE FROM feedbacks WHERE id = ?`, id)
	return err
}

// ========== Project CRUD ==========

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

// ========== Statistics & Export ==========

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

// ExportFeedbacks returns all feedbacks for a project (or all if projectID is empty) for CSV export.
func (d *Database) ExportFeedbacks(projectID string) ([]Feedback, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	const cols = `id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, is_duplicate, duplicate_of, category, created_at, updated_at`

	var rows *sql.Rows
	var err error
	if projectID != "" {
		rows, err = d.db.Query(
			`SELECT `+cols+` FROM feedbacks WHERE project_id = ? ORDER BY created_at DESC`, projectID)
	} else {
		rows, err = d.db.Query(
			`SELECT `+cols+` FROM feedbacks ORDER BY created_at DESC`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Feedback
	for rows.Next() {
		var f Feedback
		var createdAt int64
		var isDuplicate int
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &f.TrackingToken, &f.Priority, &isDuplicate, &f.DuplicateOf, &f.Category, &createdAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		f.IsDuplicate = isDuplicate == 1
		f.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, f)
	}
	return list, nil
}

// ========== Audit Logs ==========

// InsertAuditLog inserts a new audit log entry.
func (d *Database) InsertAuditLog(action, detail, user, ip string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(
		`INSERT INTO audit_logs (action, detail, user, ip) VALUES (?, ?, ?, ?)`,
		action, detail, user, ip,
	)
	return err
}

// ListAuditLogs returns recent audit log entries.
func (d *Database) ListAuditLogs(limit, offset int) ([]AuditLog, int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var total int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := d.db.Query(
		`SELECT id, action, detail, user, ip, created_at FROM audit_logs ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []AuditLog
	for rows.Next() {
		var a AuditLog
		var createdAt int64
		if err := rows.Scan(&a.ID, &a.Action, &a.Detail, &a.User, &a.IP, &createdAt); err != nil {
			return nil, 0, err
		}
		a.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, a)
	}
	return list, total, nil
}

// ========== Health Check ==========

// Ping checks if the database is responsive.
func (d *Database) Ping() error {
	return d.db.Ping()
}

// ========== Config Helpers ==========

// GetConfigByPrefix returns all config entries whose key starts with the given prefix.
func (d *Database) GetConfigByPrefix(prefix string) ([]DBConfig, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(
		`SELECT key, value, description FROM config WHERE key LIKE ? ORDER BY key`,
		prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []DBConfig
	for rows.Next() {
		var c DBConfig
		if err := rows.Scan(&c.Key, &c.Value, &c.Description); err != nil {
			return nil, err
		}
		configs = append(configs, c)
	}
	return configs, nil
}

// SetMaxOpenConns wraps sql.DB.SetMaxOpenConns for connection pool tuning.
func (d *Database) SetMaxOpenConns(n int) {
	d.db.SetMaxOpenConns(n)
}

// Close closes the database connection.
func (d *Database) Close() error {
	return d.db.Close()
}

// ========== Feedback Notes ==========

// InsertFeedbackNote adds a note/reply to a feedback.
func (d *Database) InsertFeedbackNote(feedbackID int64, content, author string, isPublic bool) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	pub := 0
	if isPublic {
		pub = 1
	}
	res, err := d.db.Exec(
		`INSERT INTO feedback_notes (feedback_id, content, author, is_public) VALUES (?, ?, ?, ?)`,
		feedbackID, content, author, pub,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListFeedbackNotes returns all notes for a feedback, ordered by creation time.
func (d *Database) ListFeedbackNotes(feedbackID int64) ([]FeedbackNote, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(
		`SELECT id, feedback_id, content, author, is_public, created_at FROM feedback_notes WHERE feedback_id = ? ORDER BY created_at ASC`,
		feedbackID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []FeedbackNote
	for rows.Next() {
		var n FeedbackNote
		var createdAt int64
		var isPublic int
		if err := rows.Scan(&n.ID, &n.FeedbackID, &n.Content, &n.Author, &isPublic, &createdAt); err != nil {
			return nil, err
		}
		n.IsPublic = isPublic == 1
		n.CreatedAt = time.Unix(createdAt, 0)
		notes = append(notes, n)
	}
	return notes, nil
}

// DeleteFeedbackNote removes a note by ID.
func (d *Database) DeleteFeedbackNote(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`DELETE FROM feedback_notes WHERE id = ?`, id)
	return err
}

// ========== Feedback Assignee ==========

// UpdateFeedbackAssignee updates the assignee field of a feedback.
func (d *Database) UpdateFeedbackAssignee(id int64, assignee string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`UPDATE feedbacks SET assignee = ?, updated_at = strftime('%s', 'now') WHERE id = ?`, assignee, id)
	return err
}

// ========== Bulk Operations ==========

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

// BulkUpdateFeedbackStatus updates status for multiple feedbacks.
func (d *Database) BulkUpdateFeedbackStatus(ids []int64, status string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, status)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := `UPDATE feedbacks SET status = ?, updated_at = strftime('%s', 'now') WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	res, err := d.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ========== Chart Data ==========

// GetDailyTrend returns feedback counts per day for the last N days.
func (d *Database) GetDailyTrend(days int) ([]map[string]interface{}, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT date(created_at, 'unixepoch') as day, COUNT(*) as cnt
		FROM feedbacks
		WHERE created_at >= strftime('%s', 'now', '-' || ? || ' days')
		GROUP BY day ORDER BY day ASC
	`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var day string
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"date":  day,
			"count": count,
		})
	}
	return result, nil
}

// GetStatusDistribution returns feedback counts grouped by status.
func (d *Database) GetStatusDistribution() ([]map[string]interface{}, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT status, COUNT(*) as cnt FROM feedbacks GROUP BY status ORDER BY cnt DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"status": status,
			"count":  count,
		})
	}
	return result, nil
}

// ========== Backup ==========

// BackupDatabase creates a backup copy of the SQLite database file.
func (d *Database) BackupDatabase(backupDir string) (string, error) {
	// Use SQLite's VACUUM INTO for a consistent backup
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	backupName := fmt.Sprintf("feedbacks_%s.db", time.Now().Format("20060102_150405"))
	backupPath := filepath.Join(backupDir, backupName)

	d.mu.Lock()
	defer d.mu.Unlock()

	// VACUUM INTO requires a string literal, not a bound parameter
	safePath := strings.ReplaceAll(backupPath, "'", "''")
	_, err := d.db.Exec(fmt.Sprintf("VACUUM INTO '%s'", safePath))
	if err != nil {
		return "", fmt.Errorf("vacuum into backup: %w", err)
	}

	return backupPath, nil
}

// ========== Tracking (Submitter Self-Service) ==========

// GetFeedbackByTrackingToken looks up a feedback by its tracking token.
// Returns nil if not found or token is empty.
func (d *Database) GetFeedbackByTrackingToken(token string) (*Feedback, error) {
	if token == "" {
		return nil, fmt.Errorf("empty tracking token")
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	var f Feedback
	var createdAt int64
	var isDuplicate int
	err := d.db.QueryRow(
		`SELECT id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, is_duplicate, duplicate_of, category, created_at, updated_at
		 FROM feedbacks WHERE tracking_token = ?`, token,
	).Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &f.TrackingToken, &f.Priority, &isDuplicate, &f.DuplicateOf, &f.Category, &createdAt, &f.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f.IsDuplicate = isDuplicate == 1
	f.CreatedAt = time.Unix(createdAt, 0)
	return &f, nil
}

// InsertSubmitterReply adds a public reply from the feedback submitter.
func (d *Database) InsertSubmitterReply(feedbackID int64, content string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	res, err := d.db.Exec(
		`INSERT INTO feedback_notes (feedback_id, content, author, is_public) VALUES (?, ?, ?, 1)`,
		feedbackID, content, "提交者",
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ========== Admin CRUD ==========

// CreateAdmin inserts a new admin account. Returns the new ID.
func (d *Database) CreateAdmin(username, passwordHash, role string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	res, err := d.db.Exec(
		`INSERT INTO admins (username, password_hash, role) VALUES (?, ?, ?)`,
		username, passwordHash, role,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetAdminByUsername looks up an admin by username. Returns nil if not found.
func (d *Database) GetAdminByUsername(username string) (*Admin, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var a Admin
	var createdAt int64
	var isActive int
	err := d.db.QueryRow(
		`SELECT id, username, password_hash, role, is_active, created_at FROM admins WHERE username = ?`, username,
	).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &isActive, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.IsActive = isActive == 1
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
	err := d.db.QueryRow(
		`SELECT id, username, password_hash, role, is_active, created_at FROM admins WHERE id = ?`, id,
	).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &isActive, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.IsActive = isActive == 1
	a.CreatedAt = time.Unix(createdAt, 0)
	return &a, nil
}

// ListAdmins returns all admin accounts.
func (d *Database) ListAdmins() ([]Admin, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT id, username, password_hash, role, is_active, created_at FROM admins ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Admin
	for rows.Next() {
		var a Admin
		var createdAt int64
		var isActive int
		if err := rows.Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &isActive, &createdAt); err != nil {
			return nil, err
		}
		a.IsActive = isActive == 1
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

// CountAdmins returns the total number of admin accounts.
func (d *Database) CountAdmins() (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var count int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&count)
	return count, err
}

// ========== Priority & Duplicate ==========

// UpdateFeedbackPriority updates the priority field of a feedback.
func (d *Database) UpdateFeedbackPriority(id int64, priority string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`UPDATE feedbacks SET priority = ?, updated_at = strftime('%s', 'now') WHERE id = ?`, priority, id)
	return err
}

// MarkAsDuplicate marks a feedback as a duplicate of another feedback.
func (d *Database) MarkAsDuplicate(id int64, duplicateOf int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`UPDATE feedbacks SET is_duplicate = 1, duplicate_of = ?, updated_at = strftime('%s', 'now') WHERE id = ?`, duplicateOf, id)
	return err
}

// UnmarkDuplicate clears the duplicate flag on a feedback.
func (d *Database) UnmarkDuplicate(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`UPDATE feedbacks SET is_duplicate = 0, duplicate_of = 0, updated_at = strftime('%s', 'now') WHERE id = ?`, id)
	return err
}

// GetAssignees returns distinct non-empty assignee values.
func (d *Database) GetAssignees() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT DISTINCT assignee FROM feedbacks WHERE assignee != '' ORDER BY assignee`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, nil
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
// Returns nil if the admin has no grants (meaning no restriction — can see all).
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
	roleLevel := map[string]int{"viewer": 1, "editor": 2, "manager": 3, "admin": 4}
	bestRole := ""
	bestLevel := 0
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
		if cat == categoryKey && lvl > bestLevel {
			bestLevel = lvl
			bestRole = role
		} else if cat == "*" && lvl > bestLevel {
			bestLevel = lvl
			bestRole = role
		}
	}
	return bestRole
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

// ========== Categories ==========

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

	// Clear category on any feedbacks referencing this category
	d.db.Exec(`UPDATE feedbacks SET category = '' WHERE project_id = ? AND category = ?`, projectSlug, key)

	_, err = d.db.Exec(`DELETE FROM categories WHERE id = ?`, id)
	return err
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

// ========== API Tokens ==========

// APIToken represents an API key for external system integration.
type APIToken struct {
	ID         int64     `json:"id"`
	Token      string    `json:"token"`
	Name       string    `json:"name"`
	ProjectID  string    `json:"project_id"`
	IsActive   bool      `json:"is_active"`
	LastUsedAt string    `json:"last_used_at"`
	CreatedAt  time.Time `json:"created_at"`
}

func (d *Database) CreateAPIToken(token, name, projectID string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.db.Exec(`INSERT INTO api_tokens (token, name, project_id) VALUES (?, ?, ?)`, token, name, projectID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *Database) ListAPITokens() ([]APIToken, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT id, token, name, project_id, is_active, COALESCE(last_used_at, ''), created_at FROM api_tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []APIToken
	for rows.Next() {
		var t APIToken
		var isActive int
		var createdAt int64
		if err := rows.Scan(&t.ID, &t.Token, &t.Name, &t.ProjectID, &isActive, &t.LastUsedAt, &createdAt); err != nil {
			return nil, err
		}
		t.IsActive = isActive == 1
		t.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, t)
	}
	return list, nil
}

func (d *Database) GetAPITokenByToken(token string) (*APIToken, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var t APIToken
	var isActive int
	var createdAt int64
	err := d.db.QueryRow(`SELECT id, token, name, project_id, is_active, COALESCE(last_used_at, ''), created_at FROM api_tokens WHERE token = ?`, token).
		Scan(&t.ID, &t.Token, &t.Name, &t.ProjectID, &isActive, &t.LastUsedAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.IsActive = isActive == 1
	t.CreatedAt = time.Unix(createdAt, 0)
	if !t.IsActive {
		return nil, nil
	}
	return &t, nil
}

func (d *Database) UpdateAPIToken(id int64, name, projectID string, isActive *bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	setClauses := []string{}
	args := []interface{}{}

	if name != "" {
		setClauses = append(setClauses, "name = ?")
		args = append(args, name)
	}
	if projectID != "" {
		setClauses = append(setClauses, "project_id = ?")
		args = append(args, projectID)
	}
	if isActive != nil {
		active := 0
		if *isActive {
			active = 1
		}
		setClauses = append(setClauses, "is_active = ?")
		args = append(args, active)
	}

	if len(setClauses) == 0 {
		return nil
	}

	args = append(args, id)
	_, err := d.db.Exec(`UPDATE api_tokens SET `+strings.Join(setClauses, ", ")+` WHERE id = ?`, args...)
	return err
}

func (d *Database) DeleteAPIToken(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM api_tokens WHERE id = ?`, id)
	return err
}

func (d *Database) TouchAPIToken(token string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`UPDATE api_tokens SET last_used_at = strftime('%s', 'now') WHERE token = ?`, token)
}

// ========== Bulk Operations (Extended) ==========

func (d *Database) BulkUpdateFeedbackTags(ids []int64, tags string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(ids) == 0 {
		return 0, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, tags)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	res, err := d.db.Exec(`UPDATE feedbacks SET tags = ?, updated_at = strftime('%s', 'now') WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (d *Database) BulkUpdateFeedbackAssignee(ids []int64, assignee string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(ids) == 0 {
		return 0, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, assignee)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	res, err := d.db.Exec(`UPDATE feedbacks SET assignee = ?, updated_at = strftime('%s', 'now') WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (d *Database) BulkUpdateFeedbackPriority(ids []int64, priority string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(ids) == 0 {
		return 0, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, priority)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	res, err := d.db.Exec(`UPDATE feedbacks SET priority = ?, updated_at = strftime('%s', 'now') WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ========== CSV Import ==========

func (d *Database) ImportFeedback(f *Feedback, createdAtUnix int64) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	status := f.Status
	if status == "" {
		status = "pending"
	}
	ts := createdAtUnix
	if ts <= 0 {
		ts = time.Now().Unix()
	}
	res, err := d.db.Exec(
		`INSERT INTO feedbacks (project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, category, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ProjectID, f.Title, f.Description, f.CustomData, f.FilePaths, f.ClientIP, status, f.Tags, f.Assignee, f.ContactName, f.ContactEmail, f.TrackingToken, f.Priority, f.Category, ts,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	f.ID = id
	return id, nil
}

// ========== Data Archiving & Cleanup ==========

func (d *Database) ArchiveOldFeedbacks(daysOld int) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cutoff := time.Now().AddDate(0, 0, -daysOld).Unix()
	res, err := d.db.Exec(`UPDATE feedbacks SET status = 'closed', updated_at = strftime('%s', 'now') WHERE status IN ('pending', 'processing') AND created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (d *Database) PruneOldBackups(backupDir string, daysOld int) (int, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().AddDate(0, 0, -daysOld)
	pruned := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(backupDir, entry.Name()))
			pruned++
		}
	}
	return pruned, nil
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

// ProjectAccess represents a user's access to a project with optional category restrictions.
type ProjectAccess struct {
	Slug             string
	AllowedCategories []string // nil = all categories (wildcard '*'), empty = no access
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

// ========== Project Archive ==========

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
