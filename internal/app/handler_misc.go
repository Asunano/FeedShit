package app

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"feedshit/internal/database"
	"feedshit/internal/middleware"
)

// ========== Password Hashing Helpers ==========

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func isBcryptHash(s string) bool {
	return len(s) == 60 && s[0] == '$'
}

func checkPassword(password, stored string) bool {
	if isBcryptHash(stored) {
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil
	}
	// Legacy plaintext comparison (for migration)
	return middleware.SecureCompare(password, stored)
}

// validatePasswordStrength checks password meets minimum complexity requirements.
// Requires: >= 8 chars, at least one uppercase, one lowercase, one digit.
func validatePasswordStrength(password string) error {
	if len(password) < 8 {
		return fmt.Errorf("密码长度至少 8 位")
	}
	var hasUpper, hasLower, hasDigit bool
	for _, c := range password {
		switch {
		case c >= 'A' && c <= 'Z':
			hasUpper = true
		case c >= 'a' && c <= 'z':
			hasLower = true
		case c >= '0' && c <= '9':
			hasDigit = true
		}
	}
	if !hasUpper {
		return fmt.Errorf("密码须包含至少一个大写字母")
	}
	if !hasLower {
		return fmt.Errorf("密码须包含至少一个小写字母")
	}
	if !hasDigit {
		return fmt.Errorf("密码须包含至少一个数字")
	}
	return nil
}

// checkFeedbackWritePerm verifies the current user can write to a feedback.
// Returns the feedback and an optional deny message. If deny is non-empty, permission is denied.
func (a *App) checkFeedbackWritePerm(c *gin.Context, fbID int64) (*database.Feedback, string) {
	roleStr, _ := c.Get("admin_role")
	if roleStr == "admin" {
		fb, err := a.DB.GetFeedback(fbID)
		if err != nil {
			return nil, "" // not found, not denied → handler returns 404
		}
		return fb, ""
	}

	fb, err := a.DB.GetFeedback(fbID)
	if err != nil {
		return nil, "" // not found, not denied → handler returns 404
	}

	username, _ := c.Get("admin_user")
	if usernameStr, ok := username.(string); ok {
		admin, _ := a.DB.GetAdminByUsername(usernameStr)
		if admin != nil {
			effectiveRole := a.DB.GetEffectiveRole(admin.ID, fb.ProjectID, fb.Category)
			if effectiveRole == "" {
				// No grant for this project — deny
				return fb, "您没有该项目的编辑权限"
			}
			roleLevel := map[string]int{"viewer": 1, "editor": 2, "manager": 3, "admin": 4}
			if roleLevel[effectiveRole] < 2 { // need editor (2) or higher
				return fb, "权限不足，需要编辑者及以上角色"
			}
		}
	}
	return fb, ""
}

// checkFeedbackReadPerm verifies the current user can read a feedback.
// Returns (feedback, denyMessage). If feedback is nil and deny is empty, the feedback doesn't exist.
// If deny is non-empty, permission is denied.
func (a *App) checkFeedbackReadPerm(c *gin.Context, fbID int64) (*database.Feedback, string) {
	roleStr, _ := c.Get("admin_role")
	if roleStr == "admin" {
		fb, err := a.DB.GetFeedback(fbID)
		if err != nil {
			return nil, "" // not found, not denied
		}
		return fb, ""
	}

	fb, err := a.DB.GetFeedback(fbID)
	if err != nil {
		return nil, "" // not found, not denied
	}

	username, _ := c.Get("admin_user")
	if usernameStr, ok := username.(string); ok {
		admin, _ := a.DB.GetAdminByUsername(usernameStr)
		if admin != nil {
			effectiveRole := a.DB.GetEffectiveRole(admin.ID, fb.ProjectID, fb.Category)
			if effectiveRole == "" {
				return fb, "您没有访问该反馈的权限"
			}
		}
	}
	return fb, ""
}

// allowedExtensions defines the file types accepted for upload.
var allowedExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".svg": true,
	".log": true, ".txt": true, ".csv": true, ".json": true,
}

func (a *App) saveUpload(fh *multipart.FileHeader, projectID string) (string, error) {
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !allowedExtensions[ext] {
		return "", fmt.Errorf("不允许的文件类型: %s", ext)
	}

	// Validate file content via magic bytes
	src, err := fh.Open()
	if err != nil {
		return "", fmt.Errorf("打开上传文件失败: %w", err)
	}
	defer src.Close()

	header := make([]byte, 512)
	n, _ := src.Read(header)
	header = header[:n]

	if !validateFileContent(header, ext) {
		return "", fmt.Errorf("文件内容与扩展名 %s 不匹配", ext)
	}

	// Reset file pointer for copying
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("重置文件指针失败: %w", err)
	}

	origName := filepath.Base(fh.Filename)
	ts := time.Now().UTC().Format("20060102_150405")
	uid := uuid.New().String()[:8]
	safeName := fmt.Sprintf("%s_%s_%s", ts, uid, origName)

	now := time.Now()
	dateDir := fmt.Sprintf("%s/%s/%s/%s", projectID,
		now.Format("2006"),
		now.Format("01"),
		now.Format("02"))
	relDir := filepath.Join("uploads", dateDir, uid)
	absDir := filepath.Join(a.Cfg.DataDir, relDir)
	if err := os.MkdirAll(absDir, 0755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}

	relPath := filepath.Join(relDir, safeName)
	absPath := filepath.Join(a.Cfg.DataDir, relPath)

	dst, err := os.Create(absPath)
	if err != nil {
		return "", fmt.Errorf("创建文件失败: %w", err)
	}
	defer dst.Close()

	// For SVG files, sanitize content before saving to prevent XSS
	if ext == ".svg" {
		fullContent, err := io.ReadAll(src)
		if err != nil {
			return "", fmt.Errorf("读取SVG内容失败: %w", err)
		}
		sanitized := sanitizeSVG(string(fullContent))
		if _, err := dst.WriteString(sanitized); err != nil {
			return "", fmt.Errorf("写入SVG文件失败: %w", err)
		}
	} else {
		if _, err := io.Copy(dst, src); err != nil {
			return "", fmt.Errorf("写入文件失败: %w", err)
		}
	}

	return filepath.ToSlash(relPath), nil
}

// cookieSecure determines whether auth cookies should be marked Secure,
// based on whether the request is (or is behind a proxy terminated at) HTTPS.
func (a *App) cookieSecure(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	if proto := c.GetHeader("X-Forwarded-Proto"); proto == "https" {
		return true
	}
	// AWS ELB sends X-Forwarded-Ssl: true when HTTPS is terminated at the LB.
	if c.GetHeader("X-Forwarded-Ssl") == "true" {
		return true
	}
	// Cloudflare sends CF-Visitor: {"scheme":"https"}.
	if v := c.GetHeader("CF-Visitor"); strings.Contains(v, `"scheme":"https"`) {
		return true
	}
	return strings.HasPrefix(a.Cfg.BaseURL, "https")
}

// getScopedStats returns stats scoped to the admin's member_grants.
func (a *App) getScopedStats(c *gin.Context) (total, projects, today int, err error) {
	role, _ := c.Get("admin_role")
	roleStr, _ := role.(string)
	if roleStr == "admin" {
		return a.DB.GetStats()
	}
	// Non-admin: filter by member_grants
	projectIDs := a.getAdminProjectIDs(c)
	if len(projectIDs) == 0 {
		return 0, 0, 0, nil
	}
	return a.DB.GetStatsForProjects(projectIDs)
}

// getAdminProjectIDs returns the list of project slugs the current admin has access to.
// Returns nil for admin users (no restriction).
func (a *App) getAdminProjectIDs(c *gin.Context) []string {
	role, _ := c.Get("admin_role")
	roleStr, _ := role.(string)
	if roleStr == "admin" {
		return nil
	}
	username, _ := c.Get("admin_user")
	usernameStr, _ := username.(string)
	if usernameStr == "" {
		return nil
	}
	admin, _ := a.DB.GetAdminByUsername(usernameStr)
	if admin == nil {
		return nil
	}
	plan, _ := a.DB.GetAdminAccessPlan(admin.ID)
	if plan == nil || len(plan) == 0 {
		return nil
	}
	ids := make([]string, 0, len(plan))
	for _, pa := range plan {
		ids = append(ids, pa.Slug)
	}
	return ids
}

// ========== Deep Health Check ==========

func (a *App) HealthCheck(c *gin.Context) {
	status := "ok"
	httpCode := http.StatusOK
	details := gin.H{}

	// Check DB connectivity
	if err := a.DB.Ping(); err != nil {
		status = "degraded"
		httpCode = http.StatusServiceUnavailable
		details["database"] = "unreachable: " + err.Error()
	} else {
		details["database"] = "connected"
	}

	c.JSON(httpCode, gin.H{
		"status":  status,
		"details": details,
	})
}

// ========== Public Setup & Dashboard ==========

// SetupStatus handles GET /api/v1/setup/status
func (a *App) SetupStatus(c *gin.Context) {
	val := a.DB.GetConfig("setup_complete")
	c.JSON(http.StatusOK, gin.H{
		"setup_complete": val == "true",
		"pow_difficulty": a.Cfg.PoWDifficulty,
	})
}

// DoSetup handles POST /api/v1/setup
func (a *App) DoSetup(c *gin.Context) {
	// Prevent re-running setup after completion
	if a.DB.GetConfig("setup_complete") == "true" {
		c.JSON(http.StatusForbidden, gin.H{"error": "系统已完成初始设置，无法重复操作"})
		return
	}

	var req struct {
		AdminUsername string `json:"admin_username"`
		AdminPassword string `json:"admin_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	if len(req.AdminUsername) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名至少 2 个字符"})
		return
	}
	if err := validatePasswordStrength(req.AdminPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Hash admin password with bcrypt
	hashedPwd, err := hashPassword(req.AdminPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}

	// Save admin credentials to DB
	a.Cfg.AdminUsername = req.AdminUsername
	a.Cfg.AdminPassword = hashedPwd
	a.DB.SetConfig("admin_username", req.AdminUsername, "管理员用户名")
	a.DB.SetConfig("admin_password", hashedPwd, "管理员密码（bcrypt 哈希）")

	// Also insert super admin into admins table for team management visibility
	if _, err := a.DB.CreateAdmin(req.AdminUsername, hashedPwd, "admin"); err != nil {
		log.Printf("[SETUP] Warning: failed to insert super admin into admins table: %v", err)
	}

	// Mark setup complete
	a.DB.SetConfig("setup_complete", "true", "初始安装已完成")

	c.JSON(http.StatusOK, gin.H{"message": "设置完成"})
}

// ========== Setup Helper ==========

// IsSetupComplete returns true if setup has been completed.
func (a *App) IsSetupComplete() bool {
	return a.DB.GetConfig("setup_complete") == "true"
}

// ========== File Serving ==========

func (a *App) AdminServeFile(c *gin.Context) {
	reqPath := c.Param("path")
	if len(reqPath) >= 7 && reqPath[:7] == "/files/" {
		reqPath = reqPath[7:]
	}
	if reqPath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "路径不能为空"})
		return
	}

	cleaned := filepath.Clean(reqPath)
	if strings.Contains(cleaned, "..") {
		c.JSON(http.StatusForbidden, gin.H{"error": "非法路径"})
		return
	}

	// Restrict file serving to the uploads/ subdirectory only — never the whole
	// DataDir (which also contains the SQLite DB and backup snapshots).
	baseDir := filepath.Join(a.Cfg.DataDir, "uploads")
	absPath := filepath.Join(baseDir, cleaned)
	// EvalSymlinks resolves symlinks to prevent symlink-based path traversal
	absResolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
		return
	}
	absDataDirResolved, err := filepath.EvalSymlinks(a.Cfg.DataDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "路径解析失败"})
		return
	}
	absBaseResolved := filepath.Join(absDataDirResolved, "uploads")
	if !strings.HasPrefix(absResolved, absBaseResolved+string(os.PathSeparator)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "非法路径"})
		return
	}

	info, err := os.Stat(absResolved)
	if err != nil || info.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
		return
	}

	c.File(absResolved)
}

// checkBulkWritePerm verifies the current user has write permission on ALL
// feedback IDs in the batch. Returns the deny message (empty = all allowed).
// This prevents non-admin users with project-specific grants from modifying
// feedbacks in projects they don't have access to.
//
// Uses batch query (GetFeedbacksByIDs) and cached admin lookup to avoid N+1.
func (a *App) checkBulkWritePerm(c *gin.Context, ids []int64) string {
	roleStr, _ := c.Get("admin_role")
	if roleStr == "admin" {
		return "" // admin has full access
	}
	if len(ids) == 0 {
		return ""
	}

	// Single admin lookup for the batch
	username, _ := c.Get("admin_user")
	usernameStr, _ := username.(string)
	if usernameStr == "" {
		return "无法验证用户身份"
	}
	admin, err := a.DB.GetAdminByUsername(usernameStr)
	if err != nil || admin == nil {
		return "用户不存在"
	}

	// Batch query all feedbacks at once
	feedbacks, err := a.DB.GetFeedbacksByIDs(ids)
	if err != nil {
		return "查询反馈失败"
	}

	roleLevel := map[string]int{"viewer": 1, "editor": 2, "manager": 3, "admin": 4}
	for _, fb := range feedbacks {
		effectiveRole := a.DB.GetEffectiveRole(admin.ID, fb.ProjectID, fb.Category)
		if roleLevel[effectiveRole] < 2 { // need editor (2) or higher
			return fmt.Sprintf("反馈 #%d: 权限不足", fb.ID)
		}
	}
	return ""
}

// checkProjectWritePerm reports whether the current user has editor+ permission
// on the given project (for operations like CSV import where no feedback ID exists yet).
// Admin users always pass; non-admin users must have a member_grant with role >= editor.
func (a *App) checkProjectWritePerm(c *gin.Context, projectSlug string) bool {
	roleStr, _ := c.Get("admin_role")
	if roleStr == "admin" {
		return true
	}
	username, _ := c.Get("admin_user")
	usernameStr, _ := username.(string)
	if usernameStr == "" {
		return false
	}
	admin, err := a.DB.GetAdminByUsername(usernameStr)
	if err != nil || admin == nil {
		return false
	}
	effectiveRole := a.DB.GetEffectiveRole(admin.ID, projectSlug, "*")
	roleLevel := map[string]int{"viewer": 1, "editor": 2, "manager": 3, "admin": 4}
	return roleLevel[effectiveRole] >= 2
}

// formSchemaField represents a single field definition in the form_schema JSON.
type formSchemaField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
}

// validateFormSchema validates custom_data JSON against the project's form_schema.
// Returns nil if the schema is empty/valid, or an error describing the first violation.
func validateFormSchema(schemaJSON, customDataJSON string) error {
	if schemaJSON == "" || schemaJSON == "[]" {
		return nil // no schema = no validation
	}
	if customDataJSON == "" {
		customDataJSON = "{}"
	}
	var schema []formSchemaField
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return nil // invalid schema on project side, skip validation
	}
	if len(schema) == 0 {
		return nil
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(customDataJSON), &data); err != nil {
		return fmt.Errorf("自定义字段格式错误")
	}

	for _, field := range schema {
		val, exists := data[field.Key]
		if field.Required {
			if !exists || val == nil || val == "" {
				return fmt.Errorf("字段 %q 为必填", field.Label)
			}
		}
		if exists && val != nil && val != "" {
			strVal := fmt.Sprintf("%v", val)
			// Type validation
			switch field.Type {
			case "number":
				if _, err := strconv.ParseFloat(strVal, 64); err != nil {
					return fmt.Errorf("字段 %q 必须为数字", field.Label)
				}
			case "select", "radio":
				valid := false
				for _, opt := range field.Options {
					if strVal == opt {
						valid = true
						break
					}
				}
				if !valid {
					return fmt.Errorf("字段 %q 的值无效", field.Label)
				}
			case "email":
				if !strings.Contains(strVal, "@") || !strings.Contains(strVal, ".") {
					return fmt.Errorf("字段 %q 必须为有效的邮箱地址", field.Label)
				}
			}
		}
	}
	return nil
}
