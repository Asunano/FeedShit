package database

import (
	"database/sql"
	"fmt"
	"log"

	"feedshit/internal/config"
	"feedshit/internal/security"
)

// GetConfig retrieves a config value by key. Returns empty string if not found.
// Sensitive values (sensitiveConfigKeys) are transparently decrypted.
func (d *Database) GetConfig(key string) string {
	var value string
	err := d.db.QueryRow(`SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return ""
	}
	if sensitiveConfigKeys[key] && security.IsEncrypted(value) {
		plain, derr := security.DecryptWithMaster(value)
		if derr == nil {
			return plain
		}
		log.Printf("WARN: failed to decrypt config key %q, returning stored value: %v", key, derr)
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
		if sensitiveConfigKeys[c.Key] && security.IsEncrypted(c.Value) {
			if plain, derr := security.DecryptWithMaster(c.Value); derr == nil {
				c.Value = plain
			}
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
		{Key: "report_recipients", Value: "", Description: "周报收件人（逗号分隔邮箱地址）"},
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
		configs = append(configs, c)
	}
	return configs, nil
}

// ExecRaw 执行原始 SQL（INSERT/UPDATE/DELETE），供 report 包内部使用。
// 调用方需自行确保语义正确；本方法仅提供互斥锁保护。
func (d *Database) ExecRaw(sql string, args ...interface{}) (sql.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.db.Exec(sql, args...)
}

// QueryRaw 执行原始 SQL 查询（SELECT），供 report 包内部使用。
// 返回 *sql.Rows，调用方必须 Close。
func (d *Database) QueryRaw(sql string, args ...interface{}) (*sql.Rows, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.db.Query(sql, args...)
}
