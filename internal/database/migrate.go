package database

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
)

// ──────────────────────────────────────────────
// Schema Migrations with version tracking
// ──────────────────────────────────────────────

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

// schemaVersion records a single idempotent migration step.
type schemaVersion struct {
	version int
	label   string
	sql     string
}

// appliedVersion returns true if the given version is already in schema_versions.
func (d *Database) appliedVersion(version int) bool {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM schema_versions WHERE version = ?`, version).Scan(&n)
	return err == nil && n > 0
}

// markVersion inserts a version record after successful migration.
func (d *Database) markVersion(version int, label string) {
	_, err := d.db.Exec(`INSERT OR IGNORE INTO schema_versions (version, label, applied_at) VALUES (?, ?, strftime('%s','now'))`, version, label)
	if err != nil {
		log.Printf("[MIGRATE] WARN: mark version %d (%s): %v", version, label, err)
	}
}

// ──────────────────────────────────────────────
// Versioned migration steps
// ──────────────────────────────────────────────

func (d *Database) migrate() error {
	// Step 0: create schema_versions table itself (no version tracking for this one)
	if _, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_versions (
			version    INTEGER PRIMARY KEY,
			label      TEXT    NOT NULL DEFAULT '',
			applied_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		)
	`); err != nil {
		return fmt.Errorf("create schema_versions: %w", err)
	}

	// All versioned migration steps in order
	migrations := []schemaVersion{
		{1, "base schema", `
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
		`},
		{2, "assignee + contact + tracking + priority columns", `
			ALTER TABLE feedbacks ADD COLUMN assignee TEXT NOT NULL DEFAULT '';
			ALTER TABLE feedbacks ADD COLUMN contact_name TEXT NOT NULL DEFAULT '';
			ALTER TABLE feedbacks ADD COLUMN contact_email TEXT NOT NULL DEFAULT '';
			ALTER TABLE feedbacks ADD COLUMN tracking_token TEXT NOT NULL DEFAULT '';
			ALTER TABLE feedbacks ADD COLUMN priority TEXT NOT NULL DEFAULT '';
		`},
		{3, "duplicate detection columns", `
			ALTER TABLE feedbacks ADD COLUMN is_duplicate INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE feedbacks ADD COLUMN duplicate_of INTEGER NOT NULL DEFAULT 0;
		`},
		{4, "content hash (M5)", `
			ALTER TABLE feedbacks ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
			CREATE INDEX IF NOT EXISTS idx_feedbacks_hash ON feedbacks(project_id, content_hash);
		`},
		{5, "category column + index", `
			ALTER TABLE feedbacks ADD COLUMN category TEXT NOT NULL DEFAULT '';
			CREATE INDEX IF NOT EXISTS idx_feedbacks_category ON feedbacks(project_id, category);
		`},
		{6, "tracking + assignee + priority indexes", `
			CREATE INDEX IF NOT EXISTS idx_feedbacks_token ON feedbacks(tracking_token);
			CREATE INDEX IF NOT EXISTS idx_feedbacks_assignee ON feedbacks(assignee);
			CREATE INDEX IF NOT EXISTS idx_feedbacks_priority ON feedbacks(priority);
		`},
		{7, "feedback_notes table", `
			CREATE TABLE IF NOT EXISTS feedback_notes (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				feedback_id INTEGER NOT NULL,
				content     TEXT    NOT NULL,
				author      TEXT    NOT NULL DEFAULT '',
				is_public   INTEGER NOT NULL DEFAULT 0,
				created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
			);
			CREATE INDEX IF NOT EXISTS idx_notes_feedback ON feedback_notes(feedback_id);
		`},
		{8, "admins table", `
			CREATE TABLE IF NOT EXISTS admins (
				id            INTEGER PRIMARY KEY AUTOINCREMENT,
				username      TEXT    NOT NULL UNIQUE,
				password_hash TEXT    NOT NULL,
				role          TEXT    NOT NULL DEFAULT 'editor',
				is_active     INTEGER NOT NULL DEFAULT 1,
				created_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
			);
		`},
		{9, "api_tokens table", `
			CREATE TABLE IF NOT EXISTS api_tokens (
				id            INTEGER PRIMARY KEY AUTOINCREMENT,
				token         TEXT    NOT NULL UNIQUE,
				name          TEXT    NOT NULL DEFAULT '',
				project_id    TEXT    NOT NULL DEFAULT '',
				is_active     INTEGER NOT NULL DEFAULT 1,
				last_used_at  TEXT    NOT NULL DEFAULT '',
				created_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
			);
		`},
		{10, "is_archived column", `
			ALTER TABLE projects ADD COLUMN is_archived INTEGER NOT NULL DEFAULT 0;
		`},
		{11, "member_grants table", `
			CREATE TABLE IF NOT EXISTS member_grants (
				id            INTEGER PRIMARY KEY AUTOINCREMENT,
				admin_id      INTEGER NOT NULL,
				project_slug  TEXT    NOT NULL,
				category_key  TEXT    NOT NULL DEFAULT '*',
				role          TEXT    NOT NULL DEFAULT 'viewer',
				UNIQUE(admin_id, project_slug, category_key)
			);
			CREATE INDEX IF NOT EXISTS idx_grants_admin ON member_grants(admin_id);
		`},
		{12, "drop project_members", `
			DROP TABLE IF EXISTS project_members;
		`},
		{13, "slug_history table", `
			CREATE TABLE IF NOT EXISTS slug_history (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				old_slug    TEXT    NOT NULL UNIQUE,
				project_slug TEXT   NOT NULL
			);
		`},
		{14, "categories table", `
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
		`},
		{15, "faqs table (M9)", `
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
		`},
		{16, "roadmap + public_on_roadmap (M3)", `
			ALTER TABLE feedbacks ADD COLUMN public_on_roadmap INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE feedbacks ADD COLUMN roadmap_status TEXT NOT NULL DEFAULT '';
		`},
		{17, "api_tokens quota columns (M7)", `
			ALTER TABLE api_tokens ADD COLUMN rate_limit INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE api_tokens ADD COLUMN quota_per_day INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE api_tokens ADD COLUMN daily_count INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE api_tokens ADD COLUMN daily_date TEXT NOT NULL DEFAULT '';
		`},
		{18, "feedback_ratings table (M2 CSAT)", `
			CREATE TABLE IF NOT EXISTS feedback_ratings (
				feedback_id INTEGER PRIMARY KEY,
				score       INTEGER NOT NULL,
				comment     TEXT    NOT NULL DEFAULT '',
				created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
			);
		`},
		{19, "feedback_votes table (M4)", `
			CREATE TABLE IF NOT EXISTS feedback_votes (
				feedback_id INTEGER NOT NULL,
				voter_key   TEXT    NOT NULL,
				created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
				PRIMARY KEY(feedback_id, voter_key)
			);
			CREATE INDEX IF NOT EXISTS idx_votes_feedback ON feedback_votes(feedback_id);
		`},
		{20, "webhook subscriptions + outbox (M6)", `
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
		`},
		{21, "backfill content_hash (M5)", `
			-- data migration: backfill performed in Go code below
		`},
		{22, "job_locks table (M13)", `
			CREATE TABLE IF NOT EXISTS job_locks (
				key         TEXT PRIMARY KEY,
				token       TEXT NOT NULL,
				locked_until INTEGER NOT NULL
			);
		`},
		{23, "invitation_tokens table", `
			CREATE TABLE IF NOT EXISTS invitation_tokens (
				id           INTEGER PRIMARY KEY AUTOINCREMENT,
				token        TEXT    NOT NULL UNIQUE,
				role         TEXT    NOT NULL DEFAULT 'editor',
				project_ids  TEXT    NOT NULL DEFAULT '[]',
				max_uses     INTEGER NOT NULL DEFAULT 1,
				used_count   INTEGER NOT NULL DEFAULT 0,
				created_by   TEXT    NOT NULL,
				created_at   INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
				expires_at   INTEGER NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_invite_token ON invitation_tokens(token);
		`},
		{24, "hash existing API tokens at rest", `
			-- data migration: hashed in Go code below
		`},
		{25, "admin email field", `
			ALTER TABLE admins ADD COLUMN email TEXT NOT NULL DEFAULT '';
		`},
		{26, "project announcement column", `
			ALTER TABLE projects ADD COLUMN announcement TEXT NOT NULL DEFAULT '';
		`},
		{27, "votes two-type + rating_open + status history", `
			CREATE TABLE IF NOT EXISTS feedback_votes_new (
				feedback_id INTEGER NOT NULL,
				voter_key   TEXT    NOT NULL,
				vote_type   TEXT    NOT NULL DEFAULT 'useful',
				created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
				PRIMARY KEY(feedback_id, voter_key, vote_type)
			);
			INSERT INTO feedback_votes_new (feedback_id, voter_key, vote_type, created_at)
				SELECT feedback_id, voter_key, 'useful', created_at FROM feedback_votes;
			DROP TABLE feedback_votes;
			ALTER TABLE feedback_votes_new RENAME TO feedback_votes;
			CREATE INDEX IF NOT EXISTS idx_votes_feedback ON feedback_votes(feedback_id);

			ALTER TABLE feedbacks ADD COLUMN rating_open INTEGER NOT NULL DEFAULT 0;

			CREATE TABLE IF NOT EXISTS feedback_status_history (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				feedback_id INTEGER NOT NULL,
				from_status TEXT    NOT NULL DEFAULT '',
				to_status   TEXT    NOT NULL DEFAULT '',
				changed_by  TEXT    NOT NULL DEFAULT '',
				note        TEXT    NOT NULL DEFAULT '',
				created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
			);
		CREATE INDEX IF NOT EXISTS idx_statushist_fb ON feedback_status_history(feedback_id);
	`},
		{28, "feedback_notes file_paths", `
			ALTER TABLE feedback_notes ADD COLUMN file_paths TEXT NOT NULL DEFAULT '[]';
		`},
		{29, "admin last_login_at", `
			ALTER TABLE admins ADD COLUMN last_login_at INTEGER NOT NULL DEFAULT 0;
		`},
		{30, "webhook deliveries history", `
			CREATE TABLE IF NOT EXISTS webhook_deliveries (
				id              INTEGER PRIMARY KEY AUTOINCREMENT,
				subscription_id INTEGER NOT NULL,
				event           TEXT    NOT NULL DEFAULT '',
				url             TEXT    NOT NULL DEFAULT '',
				request_body    TEXT    NOT NULL DEFAULT '',
				response_status INTEGER NOT NULL DEFAULT 0,
				response_body   TEXT    NOT NULL DEFAULT '',
				error           TEXT    NOT NULL DEFAULT '',
				created_at      INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
			);
			CREATE INDEX IF NOT EXISTS idx_deliveries_sub ON webhook_deliveries(subscription_id);
			CREATE INDEX IF NOT EXISTS idx_deliveries_created ON webhook_deliveries(created_at DESC);
		`},
		{31, "faqs view_count column (C2)", `
			ALTER TABLE faqs ADD COLUMN view_count INTEGER NOT NULL DEFAULT 0;
		`},
		{32, "feedback_votes target_type (C2)", `
			CREATE TABLE IF NOT EXISTS feedback_votes_new (
				feedback_id INTEGER NOT NULL,
				voter_key   TEXT    NOT NULL,
				vote_type   TEXT    NOT NULL DEFAULT 'useful',
				target_type TEXT    NOT NULL DEFAULT 'feedback',
				created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
				PRIMARY KEY(feedback_id, voter_key, vote_type, target_type)
			);
			INSERT INTO feedback_votes_new (feedback_id, voter_key, vote_type, target_type, created_at)
				SELECT feedback_id, voter_key, vote_type, 'feedback', created_at FROM feedback_votes;
			DROP TABLE feedback_votes;
			ALTER TABLE feedback_votes_new RENAME TO feedback_votes;
			CREATE INDEX IF NOT EXISTS idx_votes_feedback ON feedback_votes(feedback_id);
			CREATE INDEX IF NOT EXISTS idx_votes_target ON feedback_votes(target_type, feedback_id, vote_type);
		`},
		{33, "token_calls table (C3)", `
			CREATE TABLE IF NOT EXISTS token_calls (
				id        INTEGER PRIMARY KEY AUTOINCREMENT,
				token_id  INTEGER NOT NULL,
				ts        INTEGER NOT NULL,
				status    INTEGER NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_token_calls_token ON token_calls(token_id);
			CREATE INDEX IF NOT EXISTS idx_token_calls_ts ON token_calls(ts);
		`},
		{34, "projects.show_on_global_roadmap (总路线图纳入控制)", `
			ALTER TABLE projects ADD COLUMN show_on_global_roadmap INTEGER NOT NULL DEFAULT 1;
		`},
		{35, "roadmap curation fields (排序/置顶/目标日期/负责人/发布版本)", `
			ALTER TABLE feedbacks ADD COLUMN roadmap_order INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE feedbacks ADD COLUMN roadmap_target_date INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE feedbacks ADD COLUMN roadmap_owner TEXT NOT NULL DEFAULT '';
			ALTER TABLE feedbacks ADD COLUMN roadmap_release TEXT NOT NULL DEFAULT '';
		`},
		// Future migrations go here — never renumber existing entries.
	}

	for _, m := range migrations {
		if d.appliedVersion(m.version) {
			continue
		}
		if err := d.execVersionedMigration(m); err != nil {
			return fmt.Errorf("migration v%d (%s): %w", m.version, m.label, err)
		}
		d.markVersion(m.version, m.label)
		log.Printf("[MIGRATE] applied v%d: %s", m.version, m.label)
	}

	// M5 content_hash backfill: idempotent data migration outside versioned DDL.
	if err := d.BackfillContentHashes(); err != nil {
		return err
	}

	// M24: Hash existing API tokens at rest — only applies once.
	if d.appliedVersion(24) && !migratedAPITokensHashed {
		if err := d.migrateHashAPITokens(); err != nil {
			return err
		}
		migratedAPITokensHashed = true
	}

	return nil
}

// migratedAPITokensHashed prevents re-hashing on every startup after migration 24.
var migratedAPITokensHashed bool

// migrateHashAPITokens replaces any plaintext API tokens with their SHA-256 hashes.
func (d *Database) migrateHashAPITokens() error {
	rows, err := d.db.Query(`SELECT id, token FROM api_tokens`)
	if err != nil {
		return err
	}
	// Buffer all rows first, then close before updating. Executing UPDATE
	// inside rows.Next() with MaxOpenConns(1) risks connection deadlock.
	type tokenRow struct {
		id    int64
		token string
	}
	var pending []tokenRow
	for rows.Next() {
		var r tokenRow
		if err := rows.Scan(&r.id, &r.token); err != nil {
			rows.Close()
			return err
		}
		// SHA-256 hex is exactly 64 chars — skip if already hashed
		if len(r.token) == 64 {
			isHex := true
			for _, c := range r.token {
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
					isHex = false
					break
				}
			}
			if isHex {
				continue
			}
		}
		pending = append(pending, r)
	}
	rows.Close()

	var updates int
	for _, r := range pending {
		h := sha256.Sum256([]byte(r.token))
		hash := hex.EncodeToString(h[:])
		if _, err := d.db.Exec(`UPDATE api_tokens SET token = ? WHERE id = ?`, hash, r.id); err != nil {
			return err
		}
		updates++
	}
	if updates > 0 {
		log.Printf("[MIGRATE] Hashed %d existing API token(s) at rest", updates)
	}
	return nil
}

// execVersionedMigration runs one or more SQL statements for a versioned migration.
// Each statement is tried individually; "duplicate column" / "already exists" errors
// are silently ignored for backward compatibility with databases that may have partial
// schema state from the pre-versioning era.
func (d *Database) execVersionedMigration(m schemaVersion) error {
	// Split multi-statement migrations on blank-line boundaries or run as-is
	// SQLite's Exec handles multiple semicolon-separated statements.
	if _, err := d.db.Exec(m.sql); err != nil {
		// For multi-statement blocks, fallback to one-at-a-time with ignore
		if strings.Contains(err.Error(), "duplicate column") || strings.Contains(err.Error(), "already exists") {
			// Try each non-empty, non-comment line individually for partial schemas
			stmts := strings.Split(m.sql, ";")
			for _, s := range stmts {
				s = strings.TrimSpace(s)
				if s == "" || strings.HasPrefix(s, "--") {
					continue
				}
				if _, e := d.db.Exec(s); e != nil {
					if !strings.Contains(e.Error(), "duplicate column") && !strings.Contains(e.Error(), "already exists") && !strings.Contains(e.Error(), "already has") {
						return e
					}
				}
			}
			return nil
		}
		return err
	}
	return nil
}

// execMigrate is kept for backward compatibility — delegates to the versioned system.
func (d *Database) execMigrate(stmt string) error {
	if _, err := d.db.Exec(stmt); err != nil {
		if isIgnorableMigrationErr(err) {
			return nil
		}
		return fmt.Errorf("migration failed: %s: %w", stmt, err)
	}
	return nil
}

// isIgnorableMigrationErr reports whether a migration error can be safely ignored.
func isIgnorableMigrationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate column") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "already has")
}
