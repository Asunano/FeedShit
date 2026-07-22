package app

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"feedshit/internal/middleware"
)

// ========== Announcements ==========
//
// Two scopes:
//   - Global: stored as JSON in config key "home_announcement", shown on the
//     homepage and the /projects list page.
//   - Per-project: stored as JSON in projects.announcement, shown on each
//     /fb/{slug} feedback page (injected via PROJECT data).
//
// Announcement content supports plain text or sanitized HTML (bluemonday UGC
// policy) to prevent stored XSS.

const announcementConfigKey = "home_announcement"

// announcement represents either scope's payload.
type announcement struct {
	Enabled     bool   `json:"enabled"`
	Level       string `json:"level"`       // info | warning | success | danger
	ContentType string `json:"content_type"` // text | html
	Content     string `json:"content"`
	Dismissible bool   `json:"dismissible"`
	UpdatedAt   int64  `json:"updated_at"`
}

var validAnnounceLevels = map[string]bool{"info": true, "warning": true, "success": true, "danger": true}

// PublicGetAnnouncement returns the global announcement (if enabled) for public pages.
func (a *App) PublicGetAnnouncement(c *gin.Context) {
	raw := a.DB.GetConfig(announcementConfigKey)
	var ann announcement
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &ann); err != nil {
			ann = announcement{}
		}
	}
	if !ann.Enabled || strings.TrimSpace(ann.Content) == "" {
		c.JSON(http.StatusOK, gin.H{"enabled": false})
		return
	}
	if ann.ContentType == "html" {
		ann.Content = SanitizeHTML(ann.Content)
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":     true,
		"level":       ann.Level,
		"content_type": ann.ContentType,
		"content":     ann.Content,
		"dismissible": ann.Dismissible,
		"updated_at":  ann.UpdatedAt,
	})
}

// AdminGetAnnouncement returns the stored global announcement config (admin only).
func (a *App) AdminGetAnnouncement(c *gin.Context) {
	raw := a.DB.GetConfig(announcementConfigKey)
	var ann announcement
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &ann); err != nil {
			ann = announcement{}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":     ann.Enabled,
		"level":       ann.Level,
		"content_type": ann.ContentType,
		"content":     ann.Content,
		"dismissible": ann.Dismissible,
	})
}

// AdminUpdateAnnouncement saves the global announcement (admin only).
func (a *App) AdminUpdateAnnouncement(c *gin.Context) {
	var req announcement
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.Level == "" {
		req.Level = "info"
	}
	if !validAnnounceLevels[req.Level] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "公告级别无效"})
		return
	}
	if req.ContentType == "" {
		req.ContentType = "text"
	}
	if req.ContentType != "text" && req.ContentType != "html" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "内容格式无效"})
		return
	}
	if req.ContentType == "html" {
		req.Content = SanitizeHTML(req.Content)
	}
	req.UpdatedAt = time.Now().Unix()
	payload, err := json.Marshal(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "序列化失败"})
		return
	}
	if err := a.DB.SetConfig(announcementConfigKey, string(payload), "首页全局公告（JSON）"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存公告失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "公告已保存"})
}

// ========== Admin: Config Sections ==========

func (a *App) AdminGetConfig(c *gin.Context) {
	configs, err := a.DB.GetAllConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取配置失败"})
		return
	}
	for i := range configs {
		if (configs[i].Key == "smtp_pass" || configs[i].Key == "admin_password") && configs[i].Value != "" {
			configs[i].Value = "********"
		}
	}
	c.JSON(http.StatusOK, gin.H{"config": configs})
}

func (a *App) AdminUpdateConfig(c *gin.Context) {
	var req []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	for _, item := range req {
		// Skip sensitive fields that still contain masked value
		if (item.Key == "smtp_pass" || item.Key == "admin_password") && strings.Contains(item.Value, "*") {
			continue
		}
		if err := a.DB.SetConfig(item.Key, item.Value, ""); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存配置失败: " + item.Key})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "配置已保存"})
}

// AdminGetEmailConfig returns only email-related config.
func (a *App) AdminGetEmailConfig(c *gin.Context) {
	configs, err := a.DB.GetConfigByPrefix("smtp_")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取配置失败"})
		return
	}
	// Also include notify_enable
	notifyConfigs, _ := a.DB.GetConfigByPrefix("notify_")
	configs = append(configs, notifyConfigs...)

	for i := range configs {
		if configs[i].Key == "smtp_pass" && configs[i].Value != "" {
			configs[i].Value = "********"
		}
	}
	c.JSON(http.StatusOK, gin.H{"config": configs})
}

// AdminUpdateEmailConfig updates email-related config.
func (a *App) AdminUpdateEmailConfig(c *gin.Context) {
	var req []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	for _, item := range req {
		if item.Key == "smtp_pass" && strings.Contains(item.Value, "*") {
			continue
		}
		if err := a.DB.SetConfig(item.Key, item.Value, ""); err != nil {
			log.Printf("[CONFIG] 保存邮件设置 %s 失败: %v", item.Key, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("保存 %s 失败", item.Key)})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "邮件设置已保存"})
}

// AdminGetAccountConfig returns account-related info (no passwords).
func (a *App) AdminGetAccountConfig(c *gin.Context) {
	user := a.DB.GetConfig("admin_username")
	if user == "" {
		user = a.Cfg.AdminUsername
	}
	c.JSON(http.StatusOK, gin.H{
		"username": user,
	})
}

// AdminUpdateAccount changes admin username/password.
func (a *App) AdminUpdateAccount(c *gin.Context) {
	var req struct {
		Username    string `json:"username"`
		NewPassword string `json:"new_password"`
		OldPassword string `json:"old_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	// Verify old password
	dbPwd := a.DB.GetConfig("admin_password")
	effectivePwd := a.Cfg.AdminPassword
	if dbPwd != "" {
		effectivePwd = dbPwd
	}
	if !checkPassword(req.OldPassword, effectivePwd) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "当前密码错误"})
		return
	}

	if req.Username != "" {
		if len(req.Username) < 3 || len(req.Username) > 32 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "用户名长度须为 3-32 个字符"})
			return
		}
		a.Cfg.AdminUsername = req.Username
		if err := a.DB.SetConfig("admin_username", req.Username, "管理员用户名"); err != nil {
			log.Printf("[CONFIG] 保存用户名失败: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存用户名失败"})
			return
		}
	}
	if req.NewPassword != "" {
		if err := validatePasswordStrength(req.NewPassword); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		hashedPwd, err := hashPassword(req.NewPassword)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
			return
		}
		a.Cfg.AdminPassword = hashedPwd
		if err := a.DB.SetConfig("admin_password", hashedPwd, "管理员密码（bcrypt 哈希）"); err != nil {
			log.Printf("[CONFIG] 保存密码失败: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存密码失败"})
			return
		}
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_account", "修改账户信息", fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "账户信息已更新"})
}

// AdminGetSystemConfig returns system-level config.
func (a *App) AdminGetSystemConfig(c *gin.Context) {
	webhookURL := a.DB.GetConfig("webhook_url")
	if webhookURL == "" {
		webhookURL = a.Cfg.WebhookURL
	}
	webhookType := a.DB.GetConfig("webhook_type")
	if webhookType == "" {
		webhookType = "auto"
	}
	archiveDays := a.DB.GetConfig("archive_days")
	if archiveDays == "" {
		archiveDays = "0"
	}
	backupRetention := a.DB.GetConfig("backup_retention_days")
	if backupRetention == "" {
		backupRetention = "0"
	}
	cdnProvider := a.DB.GetConfig("cdn_provider")
	if cdnProvider == "" {
		cdnProvider = "auto"
	}
	trustedProxies := a.DB.GetConfig("trusted_proxies")
	c.JSON(http.StatusOK, gin.H{
		"base_url":               a.Cfg.BaseURL,
		"pow_difficulty":         a.Cfg.PoWDifficulty,
		"rate_limit_per_hr":      a.Cfg.RateLimitPerHour,
		"max_upload_mb":          a.Cfg.MaxUploadSize / 1024 / 1024,
		"webhook_url":            webhookURL,
		"webhook_url_deprecated": true,
		"webhook_type":           webhookType,
		"archive_days":           archiveDays,
		"backup_retention_days":  backupRetention,
		"cdn_provider":           cdnProvider,
		"trusted_proxies":        trustedProxies,
	})
}

// AdminUpdateSystemConfig updates system-level config.
func (a *App) AdminUpdateSystemConfig(c *gin.Context) {
	var req struct {
		BaseURL         string `json:"base_url"`
		PoWDifficulty   int    `json:"pow_difficulty"`
		RateLimit       int    `json:"rate_limit_per_hr"`
		WebhookURL      string `json:"webhook_url"`
		WebhookType     string `json:"webhook_type"`
		ArchiveDays     string `json:"archive_days"`
		BackupRetention string `json:"backup_retention_days"`
		CDNProvider     string `json:"cdn_provider"`
		TrustedProxies  string `json:"trusted_proxies"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	if err := a.saveSystemConfig(c, &req); err != nil {
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "系统设置已保存"})
}

// saveSystemConfig persists system config fields and returns on first error.
func (a *App) saveSystemConfig(c *gin.Context, req *struct {
	BaseURL         string `json:"base_url"`
	PoWDifficulty   int    `json:"pow_difficulty"`
	RateLimit       int    `json:"rate_limit_per_hr"`
	WebhookURL      string `json:"webhook_url"`
	WebhookType     string `json:"webhook_type"`
	ArchiveDays     string `json:"archive_days"`
	BackupRetention string `json:"backup_retention_days"`
	CDNProvider     string `json:"cdn_provider"`
	TrustedProxies  string `json:"trusted_proxies"`
},
) error {
	if req.BaseURL != "" {
		a.Cfg.BaseURL = req.BaseURL
		if err := a.DB.SetConfig("base_url", req.BaseURL, "系统基础 URL"); err != nil {
			log.Printf("[CONFIG] 保存 base_url 失败: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存基础 URL 失败"})
			return err
		}
	}
	if req.PoWDifficulty > 0 && req.PoWDifficulty <= 10 {
		a.Cfg.PoWDifficulty = req.PoWDifficulty
		if err := a.DB.SetConfig("pow_difficulty", strconv.Itoa(req.PoWDifficulty), "PoW 难度"); err != nil {
			log.Printf("[CONFIG] 保存 pow_difficulty 失败: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存 PoW 难度失败"})
			return err
		}
	}
	if req.RateLimit > 0 {
		a.Cfg.RateLimitPerHour = req.RateLimit
		if err := a.DB.SetConfig("rate_limit_per_hr", strconv.Itoa(req.RateLimit), "每小时提交上限"); err != nil {
			log.Printf("[CONFIG] 保存 rate_limit_per_hr 失败: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存速率限制失败"})
			return err
		}
	}
	if req.WebhookURL != "" {
		a.Cfg.WebhookURL = req.WebhookURL
		if err := a.DB.SetConfig("webhook_url", req.WebhookURL, "Webhook 通知 URL (已废弃，仅展示)"); err != nil {
			log.Printf("[CONFIG] 保存 webhook_url 失败: %v", err)
		}
		log.Printf("WARN: webhook_url is deprecated; it is stored for display only and no longer triggers outbound notifications. Use subscription-based webhooks via /api/v1/admin/webhooks instead.")
	}
	if req.WebhookType != "" {
		if err := a.DB.SetConfig("webhook_type", req.WebhookType, "Webhook 类型 (auto/feishu/dingtalk/slack/wecom)"); err != nil {
			log.Printf("[CONFIG] 保存 webhook_type 失败: %v", err)
		}
	}
	if req.ArchiveDays != "" {
		if err := a.DB.SetConfig("archive_days", req.ArchiveDays, "自动归档天数 (0=禁用)"); err != nil {
			log.Printf("[CONFIG] 保存 archive_days 失败: %v", err)
		}
	}
	if req.BackupRetention != "" {
		if err := a.DB.SetConfig("backup_retention_days", req.BackupRetention, "备份保留天数 (0=不自动清理)"); err != nil {
			log.Printf("[CONFIG] 保存 backup_retention_days 失败: %v", err)
		}
	}
	if req.CDNProvider != "" {
		if err := a.DB.SetConfig("cdn_provider", req.CDNProvider, "CDN 提供商 (auto/cloudflare/generic/none)"); err != nil {
			log.Printf("[CONFIG] 保存 cdn_provider 失败: %v", err)
		}
		middleware.SetCDNProvider(req.CDNProvider)
	}
	if req.TrustedProxies != "" {
		if err := a.DB.SetConfig("trusted_proxies", req.TrustedProxies, "可信代理 IP（逗号分隔，* 表示全部）"); err != nil {
			log.Printf("[CONFIG] 保存 trusted_proxies 失败: %v", err)
		}
		var proxies []string
		for _, p := range strings.Split(req.TrustedProxies, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				proxies = append(proxies, p)
			}
		}
		middleware.SetTrustedProxies(proxies)
	}
	return nil
}

// ========== Email Template Config ==========

// AdminGetEmailTemplate returns the custom email notification template.
func (a *App) AdminGetEmailTemplate(c *gin.Context) {
	subject := a.DB.GetConfig("email_template_subject")
	body := a.DB.GetConfig("email_template_body")
	c.JSON(http.StatusOK, gin.H{
		"subject_template": subject,
		"body_template":    body,
	})
}

// AdminUpdateEmailTemplate saves custom email notification template.
// Templates support placeholders: {{project}}, {{title}}, {{description}}, {{status}}, {{admin_url}}
func (a *App) AdminUpdateEmailTemplate(c *gin.Context) {
	var req struct {
		SubjectTemplate string `json:"subject_template"`
		BodyTemplate    string `json:"body_template"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	if req.SubjectTemplate != "" {
		if err := a.DB.SetConfig("email_template_subject", req.SubjectTemplate, "邮件通知标题模板"); err != nil {
			log.Printf("[CONFIG] 保存邮件主题模板失败: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存主题模板失败"})
			return
		}
	}
	if req.BodyTemplate != "" {
		if err := a.DB.SetConfig("email_template_body", req.BodyTemplate, "邮件通知正文模板"); err != nil {
			log.Printf("[CONFIG] 保存邮件正文模板失败: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存正文模板失败"})
			return
		}
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_email_template", "更新邮件模板", fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "邮件模板已保存"})
}
