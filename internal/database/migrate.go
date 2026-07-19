package database

import (
	"fmt"
	"log"
	"strings"
)

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
	// Add columns for existing databases. execMigrate ignores "duplicate column"
	// errors so re-running migrate on an existing DB is idempotent (fail-fast for
	// any other error).
	if err := d.execMigrate(`ALTER TABLE feedbacks ADD COLUMN assignee TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := d.execMigrate(`ALTER TABLE feedbacks ADD COLUMN contact_name TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := d.execMigrate(`ALTER TABLE feedbacks ADD COLUMN contact_email TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := d.execMigrate(`ALTER TABLE feedbacks ADD COLUMN tracking_token TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := d.execMigrate(`ALTER TABLE feedbacks ADD COLUMN priority TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := d.execMigrate(`ALTER TABLE feedbacks ADD COLUMN is_duplicate INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := d.execMigrate(`ALTER TABLE feedbacks ADD COLUMN duplicate_of INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	// M5: content fingerprint for duplicate detection (exact normalized hash)
	if err := d.execMigrate(`ALTER TABLE feedbacks ADD COLUMN content_hash TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := d.execMigrate(`CREATE INDEX IF NOT EXISTS idx_feedbacks_hash ON feedbacks(project_id, content_hash)`); err != nil {
		return err
	}
	if err := d.execMigrate(`ALTER TABLE feedbacks ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := d.execMigrate(`ALTER TABLE feedbacks ADD COLUMN category TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := d.execMigrate(`CREATE INDEX IF NOT EXISTS idx_feedbacks_category ON feedbacks(project_id, category)`); err != nil {
		return err
	}

	// Indexes for frequently queried columns
	if err := d.execMigrate(`CREATE INDEX IF NOT EXISTS idx_feedbacks_token ON feedbacks(tracking_token)`); err != nil {
		return err
	}
	if err := d.execMigrate(`CREATE INDEX IF NOT EXISTS idx_feedbacks_assignee ON feedbacks(assignee)`); err != nil {
		return err
	}
	if err := d.execMigrate(`CREATE INDEX IF NOT EXISTS idx_feedbacks_priority ON feedbacks(priority)`); err != nil {
		return err
	}

	// Feedback notes table (admin replies / internal notes)
	if err := d.execMigrate(`
		CREATE TABLE IF NOT EXISTS feedback_notes (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			feedback_id INTEGER NOT NULL,
			content     TEXT    NOT NULL,
			author      TEXT    NOT NULL DEFAULT '',
			is_public   INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		);
		CREATE INDEX IF NOT EXISTS idx_notes_feedback ON feedback_notes(feedback_id);
	`); err != nil {
		return err
	}

	// Admins table for multi-admin team support
	if err := d.execMigrate(`
		CREATE TABLE IF NOT EXISTS admins (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			username      TEXT    NOT NULL UNIQUE,
			password_hash TEXT    NOT NULL,
			role          TEXT    NOT NULL DEFAULT 'editor',
			is_active     INTEGER NOT NULL DEFAULT 1,
			created_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		);
	`); err != nil {
		return err
	}

	// API tokens for external system integration
	if err := d.execMigrate(`
		CREATE TABLE IF NOT EXISTS api_tokens (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			token         TEXT    NOT NULL UNIQUE,
			name          TEXT    NOT NULL DEFAULT '',
			project_id    TEXT    NOT NULL DEFAULT '',
			is_active     INTEGER NOT NULL DEFAULT 1,
			last_used_at  TEXT    NOT NULL DEFAULT '',
			created_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		);
	`); err != nil {
		return err
	}

	// Add is_archived column to projects (idempotent migration)
	if err := d.execMigrate(`ALTER TABLE projects ADD COLUMN is_archived INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}

	// Fine-grained member grants: (admin × project × category → role)
	if err := d.execMigrate(`
		CREATE TABLE IF NOT EXISTS member_grants (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			admin_id      INTEGER NOT NULL,
			project_slug  TEXT    NOT NULL,
			category_key  TEXT    NOT NULL DEFAULT '*',
			role          TEXT    NOT NULL DEFAULT 'viewer',
			UNIQUE(admin_id, project_slug, category_key)
		);
		CREATE INDEX IF NOT EXISTS idx_grants_admin ON member_grants(admin_id);
	`); err != nil {
		return err
	}

	// Clean up legacy project_members table if it exists (data already migrated to member_grants)
	if err := d.execMigrate(`DROP TABLE IF EXISTS project_members`); err != nil {
		return err
	}

	// Slug history: redirect old slugs after rename
	if err := d.execMigrate(`
		CREATE TABLE IF NOT EXISTS slug_history (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			old_slug    TEXT    NOT NULL UNIQUE,
			project_slug TEXT   NOT NULL
		);
	`); err != nil {
		return err
	}

	// Categories: feedback classification per project
	if err := d.execMigrate(`
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
	`); err != nil {
		return err
	}

	// FAQs: self-service knowledge base per project (M9)
	if err := d.execMigrate(`
		CREATE TABLE IF NOT EXISTS faqs (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			project_slug TEXT    NOT NULL,
			question     TEXT    NOT NULL,
			answer       TEXT    NOT NULL DEFAULT '',
			embedding    TEXT    NOT NULL DEFAULT '',
			is_active    INTEGER NOT NULL DEFAULT 1,
			sort_order   INTEGER NOT NULL DEFAULT 0,
			created_at   INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			updated_at   INTEGER NOT NULL DEFAULT 0,
			UNIQUE(project_slug, question)
		);
		CREATE INDEX IF NOT EXISTS idx_faqs_project ON faqs(project_slug);
		CREATE INDEX IF NOT EXISTS idx_faqs_active ON faqs(project_slug, is_active);
	`); err != nil {
		return err
	}

	// ===== Phase A + B schema extensions (idempotent) =====

	// M3 Roadmap: public flag + board status on feedbacks
	if err := d.execMigrate(`ALTER TABLE feedbacks ADD COLUMN public_on_roadmap INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := d.execMigrate(`ALTER TABLE feedbacks ADD COLUMN roadmap_status TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}

	// M7 API Token quota/rate columns
	if err := d.execMigrate(`ALTER TABLE api_tokens ADD COLUMN rate_limit INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := d.execMigrate(`ALTER TABLE api_tokens ADD COLUMN quota_per_day INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	// M7 API Token daily usage counter for quota enforcement
	if err := d.execMigrate(`ALTER TABLE api_tokens ADD COLUMN daily_count INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := d.execMigrate(`ALTER TABLE api_tokens ADD COLUMN daily_date TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}

	// M2 CSAT ratings
	if err := d.execMigrate(`
		CREATE TABLE IF NOT EXISTS feedback_ratings (
			feedback_id INTEGER PRIMARY KEY,
			score       INTEGER NOT NULL,
			comment     TEXT    NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		);
	`); err != nil {
		return err
	}

	// M4 Feedback votes (dedupe by feedback_id + voter_key)
	if err := d.execMigrate(`
		CREATE TABLE IF NOT EXISTS feedback_votes (
			feedback_id INTEGER NOT NULL,
			voter_key   TEXT    NOT NULL,
			created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			PRIMARY KEY(feedback_id, voter_key)
		);
		CREATE INDEX IF NOT EXISTS idx_votes_feedback ON feedback_votes(feedback_id);
	`); err != nil {
		return err
	}

	// M6 Webhook subscriptions + outbox (retry queue)
	if err := d.execMigrate(`
		CREATE TABLE IF NOT EXISTS webhook_subscriptions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			project_slug TEXT   NOT NULL DEFAULT '',
			url         TEXT    NOT NULL,
			secret      TEXT    NOT NULL DEFAULT '',
			events      TEXT    NOT NULL DEFAULT '*',
			is_active   INTEGER NOT NULL DEFAULT 1,
			created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		);
		CREATE TABLE IF NOT EXISTS webhook_outbox (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			subscription_id INTEGER NOT NULL,
			url           TEXT    NOT NULL,
			payload       TEXT    NOT NULL,
			secret        TEXT    NOT NULL DEFAULT '',
			attempts      INTEGER NOT NULL DEFAULT 0,
			next_at       INTEGER NOT NULL DEFAULT 0,
			last_error    TEXT    NOT NULL DEFAULT '',
			created_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		);
		CREATE INDEX IF NOT EXISTS idx_outbox_next ON webhook_outbox(next_at);
	`); err != nil {
		return err
	}

	// M5: backfill content_hash for existing rows (idempotent: only empty rows).
	if err := d.BackfillContentHashes(); err != nil {
		return err
	}

	// M13: job_locks 表——分布式作业锁（用于周报等定时任务去重）
	if err := d.execMigrate(`
		CREATE TABLE IF NOT EXISTS job_locks (
			key         TEXT PRIMARY KEY,
			token       TEXT NOT NULL,
			locked_until INTEGER NOT NULL
		);
	`); err != nil {
		return err
	}

	return nil
}

// execMigrate runs a single migration statement and returns nil for ignorable
// errors (e.g. "duplicate column" when re-running on an existing DB),
// propagating any other error to the caller so startup can fail fast.
func (d *Database) execMigrate(stmt string) error {
	if _, err := d.db.Exec(stmt); err != nil {
		if isIgnorableMigrationErr(err) {
			return nil
		}
		return fmt.Errorf("migration failed: %s: %w", stmt, err)
	}
	return nil
}

// isIgnorableMigrationErr reports whether a migration error can be safely
// ignored. Idempotent migrations (ALTER ADD COLUMN on an existing column,
// CREATE ... IF NOT EXISTS that already exists) produce these errors on
// databases created before the migration was introduced.
func isIgnorableMigrationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate column") ||
		strings.Contains(msg, "already exists")
}
