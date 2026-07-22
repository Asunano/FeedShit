package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Feedback status constants — used across database, app, and email layers.
const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusResolved   = "resolved"
	StatusClosed     = "closed"
)

// StatusLabels maps internal status keys to human-readable Chinese labels.
var StatusLabels = map[string]string{
	StatusPending:    "待处理",
	StatusProcessing: "处理中",
	StatusResolved:   "已解决",
	StatusClosed:     "已关闭",
}

// ValidStatuses is the set of all accepted feedback status values.
var ValidStatuses = map[string]bool{
	StatusPending:    true,
	StatusProcessing: true,
	StatusResolved:   true,
	StatusClosed:     true,
}

// ValidPriorities is the set of all accepted feedback priority values.
// "" means no priority set, which is also valid.
var ValidPriorities = map[string]bool{"": true, "low": true, "medium": true, "high": true, "urgent": true}

// Feedback represents a single feedback submission.
type Feedback struct {
	ID              int64     `json:"id"`
	ProjectID       string    `json:"project_id"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	CustomData      string    `json:"custom_data"`
	FilePaths       string    `json:"file_paths"`
	ClientIP        string    `json:"client_ip"`
	Status          string    `json:"status"`
	Tags            string    `json:"tags"`
	Assignee        string    `json:"assignee"`
	ContactName     string    `json:"contact_name"`
	ContactEmail    string    `json:"contact_email"`
	TrackingToken   string    `json:"tracking_token,omitempty"`
	Priority        string    `json:"priority"`
	IsDuplicate     bool      `json:"is_duplicate"`
	DuplicateOf     int64     `json:"duplicate_of"`
	ContentHash     string    `json:"content_hash"` // 内容指纹（归一化 SHA-256），仅内部比对，不对外暴露语义
	Category        string    `json:"category"`
	PublicOnRoadmap bool      `json:"public_on_roadmap"`
	RoadmapStatus   string    `json:"roadmap_status"`
	RoadmapOrder    int       `json:"roadmap_order"`
	RoadmapTargetDate int64   `json:"roadmap_target_date"`
	RoadmapOwner    string    `json:"roadmap_owner"`
	RoadmapRelease  string    `json:"roadmap_release"`
	Votes           int       `json:"votes"`
	UsefulVotes     int       `json:"useful_votes"`
	EncounteredVotes int      `json:"encountered_votes"`
	RatingOpen      bool      `json:"rating_open"`
	// FormSchema 仅由 AdminGetFeedback 在返回时填充（来自所属项目），非数据库列。
	FormSchema string `json:"form_schema" gorm:"-"`
	// Export-only: populated by ExportFeedbacks for CSV/JSON/XLSX output.
	NotesContent string `json:"notes_content,omitempty"`
	RatingScore  int    `json:"rating_score,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       int64     `json:"updated_at"`
}

// Admin represents a team member with login credentials.
type Admin struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	IsActive     bool      `json:"is_active"`
	LastLoginAt  int64     `json:"last_login_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// FeedbackNote represents an admin reply or internal note on a feedback.
type FeedbackNote struct {
	ID         int64     `json:"id"`
	FeedbackID int64     `json:"feedback_id"`
	Content    string    `json:"content"`
	Author     string    `json:"author"`
	IsPublic   bool      `json:"is_public"`
	FilePaths  string    `json:"file_paths"`
	CreatedAt  time.Time `json:"created_at"`
}

// Project represents a feedback collection project.
type Project struct {
	ID                  int64     `json:"id"`
	Name                string    `json:"name"`
	Slug                string    `json:"slug"`
	Description         string    `json:"description"`
	IsActive            bool      `json:"is_active"`
	IsArchived          bool      `json:"is_archived"`
	FormSchema          string    `json:"form_schema"`
	Announcement        string    `json:"announcement"`
	ShowOnGlobalRoadmap bool      `json:"show_on_global_roadmap"`
	FeedbackCount       int       `json:"feedback_count"`
	CreatedAt           time.Time `json:"created_at"`
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

// FeedbackRating holds a submitter's satisfaction score for a resolved feedback.
type FeedbackRating struct {
	FeedbackID int64     `json:"feedback_id"`
	Score      int       `json:"score"`
	Comment    string    `json:"comment"`
	CreatedAt  time.Time `json:"created_at"`
}

// StatusChange is one entry in a feedback's status-transition history.
type StatusChange struct {
	ID         int64     `json:"id"`
	FeedbackID int64     `json:"feedback_id"`
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	ChangedBy  string    `json:"changed_by"`
	Note       string    `json:"note"`
	CreatedAt  time.Time `json:"created_at"`
}

// RoadmapItem is a public-safe view of a feedback shown on the roadmap board.
type RoadmapItem struct {
	ID            int64     `json:"id"`
	Title         string    `json:"title"`
	Category      string    `json:"category"`
	Description   string    `json:"description"`
	ProjectSlug   string    `json:"project_slug"`
	RoadmapStatus string    `json:"roadmap_status"`
	Votes         int       `json:"votes"`
	CreatedAt     time.Time `json:"created_at"`
	MentionCount  int       `json:"mention_count"`
	RoadmapOrder  int       `json:"roadmap_order"`
	TargetDate    int64     `json:"target_date"`
	Owner         string    `json:"owner"`
	Release       string    `json:"release"`
}

// RoadmapAdminItem is the admin management view of roadmap entries: every
// feedback that has been placed on the board (roadmap_status set) or flagged
// public, with its public flag exposed for inline toggling.
type RoadmapAdminItem struct {
	ID              int64  `json:"id"`
	Title           string `json:"title"`
	Category        string `json:"category"`
	ProjectSlug     string `json:"project_slug"`
	RoadmapStatus   string `json:"roadmap_status"`
	PublicOnRoadmap bool   `json:"public_on_roadmap"`
	Votes           int    `json:"votes"`
	UpdatedAt       int64  `json:"updated_at"`
	MentionCount    int    `json:"mention_count"`
	RoadmapOrder    int    `json:"roadmap_order"`
	TargetDate      int64  `json:"target_date"`
	Owner           string `json:"owner"`
	Release         string `json:"release"`
}

// WebhookSubscription defines a per-project/event webhook endpoint.
type WebhookSubscription struct {
	ID          int64     `json:"id"`
	ProjectSlug string    `json:"project_slug"`
	URL         string    `json:"url"`
	Secret      string    `json:"secret"`
	Events      string    `json:"events"` // comma-separated event names, or "*"
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
}

// WebhookOutbox is a pending/retried webhook delivery.
type WebhookOutbox struct {
	ID             int64     `json:"id"`
	SubscriptionID int64     `json:"subscription_id"`
	URL            string    `json:"url"`
	Payload        string    `json:"payload"`
	Secret         string    `json:"secret"`
	Attempts       int       `json:"attempts"`
	NextAt         int64     `json:"next_at"`
	LastError      string    `json:"last_error"`
	CreatedAt      time.Time `json:"created_at"`
}

// WebhookDelivery is a recorded webhook delivery attempt (success or failure)
// kept for audit/history display in the admin UI.
type WebhookDelivery struct {
	ID             int64     `json:"id"`
	SubscriptionID int64     `json:"subscription_id"`
	Event          string    `json:"event"`
	URL            string    `json:"url"`
	RequestBody    string    `json:"request_body"`
	ResponseStatus int       `json:"response_status"`
	ResponseBody   string    `json:"response_body"`
	Error          string    `json:"error"`
	CreatedAt      time.Time `json:"created_at"`
}

// APIToken represents an API key for external system integration.
type APIToken struct {
	ID          int64     `json:"id"`
	Token       string    `json:"token"`
	Name        string    `json:"name"`
	ProjectID   string    `json:"project_id"`
	IsActive    bool      `json:"is_active"`
	RateLimit   int       `json:"rate_limit"`    // per-hour limit; 0 = unlimited
	QuotaPerDay int       `json:"quota_per_day"` // daily limit; 0 = unlimited
	LastUsedAt  string    `json:"last_used_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// ProjectAccess represents a user's access to a project with optional category restrictions.
type ProjectAccess struct {
	Slug              string
	AllowedCategories []string // nil = all categories (wildcard '*'), empty = no access
}

// sensitiveConfigKeys lists config keys whose values must be encrypted at rest.
// Reads transparently decrypt them; writes encrypt them. Non-sensitive keys
// pass through unchanged.
var sensitiveConfigKeys = map[string]bool{
	"smtp_pass": true,
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

	// Single-connection mode: SQLite serializes all operations at the driver level.
	// The manual RWMutex in Database struct is redundant for concurrency safety but
	// kept for future-proofing (e.g. multi-connection mode, connection pooling) and
	// to allow goroutine-level coordination for transactions and test isolation.
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
	// Pin to a single connection so the in-memory database is shared across all
	// queries (a new pooled connection would otherwise see a fresh empty DB).
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	d := &Database{db: db}
	if err := d.initDB(); err != nil {
		db.Close()
		return nil, err
	}
	return d, nil
}

// SetMaxOpenConns wraps sql.DB.SetMaxOpenConns for connection pool tuning.
func (d *Database) SetMaxOpenConns(n int) {
	d.db.SetMaxOpenConns(n)
}

// Close closes the database connection.
func (d *Database) Close() error {
	return d.db.Close()
}

// Ping checks if the database is responsive.
func (d *Database) Ping() error {
	return d.db.Ping()
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
	var ratingOpen int
	err := d.db.QueryRow(
		`SELECT id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, is_duplicate, duplicate_of, category, created_at, updated_at, content_hash, rating_open
		 FROM feedbacks WHERE tracking_token = ?`, token,
	).Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &f.TrackingToken, &f.Priority, &isDuplicate, &f.DuplicateOf, &f.Category, &createdAt, &f.UpdatedAt, &f.ContentHash, &ratingOpen)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f.IsDuplicate = isDuplicate == 1
	f.RatingOpen = ratingOpen == 1
	f.CreatedAt = time.Unix(createdAt, 0)
	return &f, nil
}

// InsertSubmitterReply adds a public reply from the feedback submitter.
func (d *Database) InsertSubmitterReply(feedbackID int64, content, filePaths string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	res, err := d.db.Exec(
		`INSERT INTO feedback_notes (feedback_id, content, author, is_public, file_paths) VALUES (?, ?, ?, 1, ?)`,
		feedbackID, content, "提交者", filePaths,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ========== Priority & Duplicate ==========

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

// ========== Webhook Helpers ==========

// eventMatches reports whether a subscription's event filter covers the given event.
func eventMatches(filter, event string) bool {
	if filter == "" || filter == "*" {
		return true
	}
	for _, e := range strings.Split(filter, ",") {
		if strings.TrimSpace(e) == event {
			return true
		}
	}
	return false
}
