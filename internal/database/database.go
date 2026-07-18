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
	ID           int64     `json:"id"`
	ProjectID    string    `json:"project_id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	CustomData   string    `json:"custom_data"`
	FilePaths    string    `json:"file_paths"`
	ClientIP     string    `json:"client_ip"`
	Status       string    `json:"status"`
	Tags         string    `json:"tags"`
	Assignee     string    `json:"assignee"`
	ContactName  string    `json:"contact_name"`
	ContactEmail string    `json:"contact_email"`
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
	FormSchema    string    `json:"form_schema"`
	FeedbackCount int       `json:"feedback_count"`
	CreatedAt     time.Time `json:"created_at"`
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
		created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
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
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN custom_data TEXT NOT NULL DEFAULT '{}'`)
	d.db.Exec(`ALTER TABLE projects ADD COLUMN form_schema TEXT NOT NULL DEFAULT '[]'`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN status TEXT NOT NULL DEFAULT 'pending'`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN tags TEXT NOT NULL DEFAULT ''`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN assignee TEXT NOT NULL DEFAULT ''`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN contact_name TEXT NOT NULL DEFAULT ''`)
	d.db.Exec(`ALTER TABLE feedbacks ADD COLUMN contact_email TEXT NOT NULL DEFAULT ''`)

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
		`INSERT INTO feedbacks (project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'))`,
		f.ProjectID, f.Title, f.Description, f.CustomData, f.FilePaths, f.ClientIP, status, f.Tags, f.Assignee, f.ContactName, f.ContactEmail,
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

// ListFeedbacks returns feedbacks filtered by project_id (empty = all), paginated.
func (d *Database) ListFeedbacks(projectID string, limit, offset int) ([]Feedback, int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var total int
	var rows *sql.Rows
	var err error

	const cols = `id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, created_at`

	if projectID != "" {
		err = d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks WHERE project_id = ?`, projectID).Scan(&total)
		if err != nil {
			return nil, 0, err
		}
		rows, err = d.db.Query(
			`SELECT `+cols+` FROM feedbacks WHERE project_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`,
			projectID, limit, offset,
		)
	} else {
		err = d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks`).Scan(&total)
		if err != nil {
			return nil, 0, err
		}
		rows, err = d.db.Query(
			`SELECT `+cols+` FROM feedbacks ORDER BY created_at DESC LIMIT ? OFFSET ?`,
			limit, offset,
		)
	}
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []Feedback
	for rows.Next() {
		var f Feedback
		var createdAt int64
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &createdAt); err != nil {
			return nil, 0, err
		}
		f.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, f)
	}
	return list, total, nil
}

// SearchFeedbacks supports keyword search, status filter, and project filter.
func (d *Database) SearchFeedbacks(projectID, keyword, status string, limit, offset int) ([]Feedback, int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	where := "WHERE 1=1"
	args := []interface{}{}

	if projectID != "" {
		where += " AND project_id = ?"
		args = append(args, projectID)
	}
	if status != "" {
		where += " AND status = ?"
		args = append(args, status)
	}
	if keyword != "" {
		where += " AND (title LIKE ? OR description LIKE ?)"
		args = append(args, "%"+keyword+"%", "%"+keyword+"%")
	}

	const cols = `id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, created_at`

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
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &createdAt); err != nil {
			return nil, 0, err
		}
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
	err := d.db.QueryRow(
		`SELECT id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, created_at
		 FROM feedbacks WHERE id = ?`, id,
	).Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &createdAt)
	if err != nil {
		return nil, err
	}
	f.CreatedAt = time.Unix(createdAt, 0)
	return &f, nil
}

// UpdateFeedbackStatus updates the status and/or tags of a feedback.
func (d *Database) UpdateFeedbackStatus(id int64, status, tags string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`UPDATE feedbacks SET status = ?, tags = ? WHERE id = ?`, status, tags, id)
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
	res, err := d.db.Exec(
		`INSERT INTO projects (name, slug, description, is_active, form_schema) VALUES (?, ?, ?, ?, ?)`,
		p.Name, p.Slug, p.Description, active, p.FormSchema,
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
	_, err := d.db.Exec(
		`UPDATE projects SET name = ?, slug = ?, description = ?, is_active = ?, form_schema = ? WHERE id = ?`,
		p.Name, p.Slug, p.Description, active, p.FormSchema, p.ID,
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
	var isActive int
	err := d.db.QueryRow(
		`SELECT id, name, slug, description, is_active, form_schema, created_at FROM projects WHERE id = ?`, id,
	).Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &isActive, &p.FormSchema, &createdAt)
	if err != nil {
		return nil, err
	}
	p.IsActive = isActive == 1
	p.CreatedAt = time.Unix(createdAt, 0)
	return &p, nil
}

// GetProjectBySlug returns a project by its slug.
func (d *Database) GetProjectBySlug(slug string) (*Project, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var p Project
	var createdAt int64
	var isActive int
	err := d.db.QueryRow(
		`SELECT id, name, slug, description, is_active, form_schema, created_at FROM projects WHERE slug = ?`, slug,
	).Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &isActive, &p.FormSchema, &createdAt)
	if err != nil {
		return nil, err
	}
	p.IsActive = isActive == 1
	p.CreatedAt = time.Unix(createdAt, 0)
	return &p, nil
}

// ListProjects returns all projects ordered by creation date, with feedback counts.
func (d *Database) ListProjects() ([]Project, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT id, name, slug, description, is_active, form_schema, created_at FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		var createdAt int64
		var isActive int
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.Description, &isActive, &p.FormSchema, &createdAt); err != nil {
			return nil, err
		}
		p.IsActive = isActive == 1
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

	const cols = `id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, created_at`

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
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &createdAt); err != nil {
			return nil, err
		}
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

	_, err := d.db.Exec(`UPDATE feedbacks SET assignee = ? WHERE id = ?`, assignee, id)
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
	query := `DELETE FROM feedbacks WHERE id IN (` + strings.Join(placeholders, ",") + `)`
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
	query := `UPDATE feedbacks SET status = ? WHERE id IN (` + strings.Join(placeholders, ",") + `)`
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

	_, err := d.db.Exec(`VACUUM INTO ?`, backupPath)
	if err != nil {
		return "", fmt.Errorf("vacuum into backup: %w", err)
	}

	return backupPath, nil
}
