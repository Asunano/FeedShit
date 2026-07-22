package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	"feedshit/internal/config"
	"feedshit/internal/security"
)

// GetConfig retrieves a config value by key. Returns empty string if not found.
// Sensitive values (sensitiveConfigKeys) are transparently decrypted.
func (d *Database) GetConfig(key string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var value string
	err := d.db.QueryRow(`SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return ""
	}
	return d.decryptConfigValue(key, value)
}

// decryptConfigValue returns the cleartext for a config value, transparently
// decrypting it when the key is sensitive and the stored value is encrypted.
// Non-sensitive keys, plaintext values, and decryption failures return value unchanged.
func (d *Database) decryptConfigValue(key, value string) string {
	if sensitiveConfigKeys[key] && value != "" && security.IsEncrypted(value) {
		if plain, derr := security.DecryptWithMaster(value); derr == nil {
			return plain
		} else {
			log.Printf("WARN: failed to decrypt config key %q, returning stored value: %v", key, derr)
		}
	}
	return value
}

// SetConfig upserts a config entry. Sensitive values (sensitiveConfigKeys) are
// encrypted at rest before being written; non-sensitive values are stored as-is.
func (d *Database) SetConfig(key, value, description string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	stored := value
	if sensitiveConfigKeys[key] && stored != "" && !security.IsEncrypted(stored) {
		enc, err := security.EncryptWithMaster(stored)
		if err != nil {
			return err
		}
		stored = enc
	}
	_, err := d.db.Exec(
		`INSERT INTO config (key, value, description) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, description = excluded.description`,
		key, stored, description,
	)
	return err
}

// GetAllConfig returns all config entries. Sensitive values (sensitiveConfigKeys)
// are transparently decrypted so callers (e.g. mailer, admin config view) receive
// cleartext.
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
		c.Value = d.decryptConfigValue(c.Key, c.Value)
		configs = append(configs, c)
	}
	return configs, nil
}

// InitDefaultConfig seeds default config values if they don't exist.
// Note: do NOT hold d.mu here — SetConfig (called below) acquires its own
// lock, and sync.RWMutex is not reentrant. The seeding runs once at startup
// (single goroutine), so the per-key COUNT check + SetConfig needs no external lock.
func (d *Database) InitDefaultConfig(cfg *config.Config) {
	defaults := []DBConfig{
		{Key: "smtp_host", Value: cfg.SMTPHost, Description: "SMTP 服务器地址"},
		{Key: "smtp_port", Value: fmt.Sprintf("%d", cfg.SMTPPort), Description: "SMTP 端口"},
		{Key: "smtp_user", Value: cfg.SMTPUser, Description: "SMTP 用户名"},
		{Key: "smtp_pass", Value: cfg.SMTPPass, Description: "SMTP 密码"},
		{Key: "smtp_from", Value: cfg.SMTPFrom, Description: "发件人地址"},
		{Key: "smtp_to", Value: cfg.SMTPTo, Description: "收件人地址（多个用逗号分隔）"},
		{Key: "notify_enable", Value: fmt.Sprintf("%t", cfg.NotifyEnable), Description: "是否启用邮件通知"},
		{Key: "report_recipients", Value: "", Description: "周报收件人（逗号分隔邮箱地址）"},
		{Key: "roadmap_auto_board", Value: "true", Description: "新反馈提交时是否自动上板到路线图"},
		{Key: "roadmap_default_status", Value: "planning", Description: "自动上板时的默认看板阶段(planning/in_progress/released)"},
		{Key: "roadmap_default_public", Value: "false", Description: "自动上板时是否默认公开(安全默认不公开，由管理员手动设置)"},
		{Key: "roadmap_auto_promote", Value: "true", Description: "反馈 resolved 时是否自动晋升已在板条目的状态"},
		{Key: "roadmap_auto_promote_status", Value: "released", Description: "自动晋升到的目标阶段"},
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
		c.Value = d.decryptConfigValue(c.Key, c.Value)
		configs = append(configs, c)
	}
	return configs, nil
}

// ExecRaw 执行原始 SQL（INSERT/UPDATE/DELETE），供 report 包内部使用。
//
// SECURITY CONTRACT: callers MUST pass user-supplied values exclusively via
// the args parameter (parameterized ? placeholders). Never interpolate user
// input into the sql string — doing so introduces SQL injection.
func (d *Database) ExecRaw(sql string, args ...interface{}) (sql.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.db.Exec(sql, args...)
}

// configKeyEnvMap maps config DB keys to their environment variable names.
// Used by GetConfigWithFallback to implement DB → env → default resolution.
var configKeyEnvMap = map[string]string{
	"smtp_host":               "SMTP_HOST",
	"smtp_port":               "SMTP_PORT",
	"smtp_user":               "SMTP_USER",
	"smtp_pass":               "SMTP_PASS",
	"smtp_from":               "SMTP_FROM",
	"smtp_to":                 "SMTP_TO",
	"notify_enable":           "NOTIFY_ENABLE",
	"base_url":                "BASE_URL",
	"pow_difficulty":          "POW_DIFFICULTY",
	"rate_limit_per_hr":       "RATE_LIMIT_PER_HOUR",
	"archive_days":            "ARCHIVE_DAYS",
	"backup_retention_days":   "BACKUP_RETENTION_DAYS",
	"cdn_provider":            "CDN_PROVIDER",
	"trusted_proxies":         "TRUSTED_PROXIES",
}

// GetConfigWithFallback retrieves a config value with a two-tier fallback:
//  1. DB config table (GetConfig)
//  2. Environment variable (if mapped in configKeyEnvMap)
//  3. Returns empty string if neither is set
func (d *Database) GetConfigWithFallback(key string) string {
	if v := d.GetConfig(key); v != "" {
		return v
	}
	if envKey, ok := configKeyEnvMap[key]; ok {
		if v := os.Getenv(envKey); v != "" {
			return v
		}
	}
	return ""
}

// QueryRaw 执行原始 SQL 查询（SELECT），供 report 包内部使用。
// 返回 *sql.Rows，调用方必须 Close。
//
// SECURITY CONTRACT: callers MUST pass user-supplied values exclusively via
// the args parameter (parameterized ? placeholders). Never interpolate user
// input into the sql string.
//
// SAFETY NOTE: the RLock is released when this function returns, but the
// caller still holds open Rows. This is only safe while MaxOpenConns == 1
// (the current setting). If MaxOpenConns is ever increased, restructure to
// return fully-materialized results instead of *sql.Rows.
func (d *Database) QueryRaw(sql string, args ...interface{}) (*sql.Rows, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.db.Query(sql, args...)
}
