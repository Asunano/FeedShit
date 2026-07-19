package app

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"feedshit/internal/middleware"
)

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
		a.DB.SetConfig(item.Key, item.Value, "")
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

	if req.Username != "" && len(req.Username) >= 2 {
		a.Cfg.AdminUsername = req.Username
		a.DB.SetConfig("admin_username", req.Username, "管理员用户名")
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
		a.DB.SetConfig("admin_password", hashedPwd, "管理员密码（bcrypt 哈希）")
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
	if req.BaseURL != "" {
		a.Cfg.BaseURL = req.BaseURL
		a.DB.SetConfig("base_url", req.BaseURL, "系统基础 URL")
	}
	if req.PoWDifficulty > 0 && req.PoWDifficulty <= 10 {
		a.Cfg.PoWDifficulty = req.PoWDifficulty
		a.DB.SetConfig("pow_difficulty", strconv.Itoa(req.PoWDifficulty), "PoW 难度")
	}
	if req.RateLimit > 0 {
		a.Cfg.RateLimitPerHour = req.RateLimit
		a.DB.SetConfig("rate_limit_per_hr", strconv.Itoa(req.RateLimit), "每小时提交上限")
	}
	if req.WebhookURL != "" {
		a.Cfg.WebhookURL = req.WebhookURL
		a.DB.SetConfig("webhook_url", req.WebhookURL, "Webhook 通知 URL (已废弃，仅展示)")
		log.Printf("WARN: webhook_url is deprecated; it is stored for display only and no longer triggers outbound notifications. Use subscription-based webhooks via /api/v1/admin/webhooks instead.")
	}
	if req.WebhookType != "" {
		a.DB.SetConfig("webhook_type", req.WebhookType, "Webhook 类型 (auto/feishu/dingtalk/slack/wecom)")
	}
	if req.ArchiveDays != "" {
		a.DB.SetConfig("archive_days", req.ArchiveDays, "自动归档天数 (0=禁用)")
	}
	if req.BackupRetention != "" {
		a.DB.SetConfig("backup_retention_days", req.BackupRetention, "备份保留天数 (0=不自动清理)")
	}
	if req.CDNProvider != "" {
		a.DB.SetConfig("cdn_provider", req.CDNProvider, "CDN 提供商 (auto/cloudflare/generic/none)")
		middleware.SetCDNProvider(req.CDNProvider)
	}
	if req.TrustedProxies != "" {
		a.DB.SetConfig("trusted_proxies", req.TrustedProxies, "可信代理 IP（逗号分隔，* 表示全部）")
		// Apply at runtime
		var proxies []string
		for _, p := range strings.Split(req.TrustedProxies, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				proxies = append(proxies, p)
			}
		}
		middleware.SetTrustedProxies(proxies)
	}

	c.JSON(http.StatusOK, gin.H{"message": "系统设置已保存"})
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
		a.DB.SetConfig("email_template_subject", req.SubjectTemplate, "邮件通知标题模板")
	}
	if req.BodyTemplate != "" {
		a.DB.SetConfig("email_template_body", req.BodyTemplate, "邮件通知正文模板")
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_email_template", "更新邮件模板", fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "邮件模板已保存"})
}
