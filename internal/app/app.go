package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
	"golang.org/x/crypto/bcrypt"

	"feedshit/internal/config"
	"feedshit/internal/database"
	"feedshit/internal/email"
	"feedshit/internal/middleware"
)

// App holds all shared dependencies for HTTP handlers.
type App struct {
	Cfg          *config.Config
	DB           *database.Database
	SM           *middleware.SessionManager
	RL           *middleware.RateLimiter
	Mailer       *email.Mailer
	NonceCache   *middleware.NonceCache
	LoginTracker *middleware.LoginAttemptTracker

	// M7: per-token rate limiting (in-memory, single-instance)
	tokenMu       sync.Mutex
	tokenHourHits map[string]int
}

// New creates a new App instance.
func New(cfg *config.Config, db *database.Database, sm *middleware.SessionManager, rl *middleware.RateLimiter, mailer *email.Mailer) *App {
	a := &App{
		Cfg:          cfg,
		DB:           db,
		SM:           sm,
		RL:           rl,
		Mailer:       mailer,
		NonceCache:   middleware.NewNonceCache(),
		LoginTracker: middleware.NewLoginAttemptTracker(10),
		tokenHourHits: make(map[string]int),
	}
	// Periodically clear per-token hourly hit counters to avoid unbounded memory
	// growth. Keys embed the hour string, so clearing hourly is safe — counters
	// reset each hour anyway. Fixes the in-memory leak in APITokenAuthMiddleware.
	go func() {
		ticker := time.NewTicker(time.Hour)
		for range ticker.C {
			a.tokenMu.Lock()
			a.tokenHourHits = make(map[string]int)
			a.tokenMu.Unlock()
		}
	}()
	// Load CDN config from DB at startup
	if cdn := db.GetConfig("cdn_provider"); cdn != "" {
		middleware.SetCDNProvider(cdn)
	}
	if tp := db.GetConfig("trusted_proxies"); tp != "" {
		var proxies []string
		for _, p := range strings.Split(tp, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				proxies = append(proxies, p)
			}
		}
		middleware.SetTrustedProxies(proxies)
	}
	return a
}

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

// ========== Public Submission ==========

func (a *App) SubmitFeedback(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, a.Cfg.MaxUploadSize)

	if err := c.Request.ParseMultipartForm(a.Cfg.MaxUploadSize); err != nil {
		maxMB := a.Cfg.MaxUploadSize / 1024 / 1024
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("请求体过大，上限 %dMB", maxMB)})
		return
	}

	projectID := strings.TrimSpace(c.PostForm("project_id"))
	if projectID == "" {
		projectID = "default"
	}

	// Check if project is active — must exist in projects table
	if !a.DB.IsProjectActive(projectID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "该项目不存在或已停用，无法提交反馈"})
		return
	}

	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "标题不能为空"})
		return
	}
	description := strings.TrimSpace(c.PostForm("description"))

	customData := strings.TrimSpace(c.PostForm("custom_data"))
	if customData == "" {
		customData = "{}"
	}
	// Validate custom_data is valid JSON
	if !json.Valid([]byte(customData)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "自定义字段数据格式无效"})
		return
	}

	// PoW verification with nonce replay protection
	timestamp := c.GetHeader("X-PoW-Timestamp")
	nonce := c.GetHeader("X-PoW-Nonce")
	if !middleware.VerifyPoW(projectID, timestamp, nonce, a.Cfg.PoWDifficulty) {
		c.JSON(http.StatusForbidden, gin.H{"error": "工作量证明校验失败"})
		return
	}
	// Check nonce replay
	nonceKey := projectID + ":" + timestamp + ":" + nonce
	if !a.NonceCache.CheckAndStore(nonceKey) {
		c.JSON(http.StatusForbidden, gin.H{"error": "工作量证明已被使用，请刷新页面重试"})
		return
	}

	savedPaths := make([]string, 0)
	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "解析表单失败"})
		return
	}

	for _, fh := range form.File["images"] {
		p, err := a.saveUpload(fh, projectID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "文件校验失败: " + err.Error()})
			return
		}
		savedPaths = append(savedPaths, p)
	}
	for _, fh := range form.File["files"] {
		p, err := a.saveUpload(fh, projectID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "文件校验失败: " + err.Error()})
			return
		}
		savedPaths = append(savedPaths, p)
	}

	filePathsJSON, _ := json.Marshal(savedPaths)
	clientIP := middleware.GetClientIP(c)

	contactName := strings.TrimSpace(c.PostForm("contact_name"))
	contactEmail := strings.TrimSpace(c.PostForm("contact_email"))

	// Validate category against project dictionary
	category := strings.TrimSpace(c.PostForm("category"))
	if category != "" {
		cat, catErr := a.DB.GetCategoryByKey(projectID, category)
		if catErr != nil || cat == nil || !cat.IsActive {
			c.JSON(http.StatusBadRequest, gin.H{"error": "分类无效或不存在于该项目字典中"})
			return
		}
	}

	// Generate tracking token for submitter self-service
	tokenBytes := make([]byte, 16)
	rand.Read(tokenBytes)
	trackingToken := hex.EncodeToString(tokenBytes)

	fb := &database.Feedback{
		ProjectID:     projectID,
		Title:         title,
		Description:   description,
		CustomData:    customData,
		FilePaths:     string(filePathsJSON),
		ClientIP:      clientIP,
		Status:        "pending",
		ContactName:   contactName,
		ContactEmail:  contactEmail,
		TrackingToken: trackingToken,
		Category:      category,
	}

	id, err := a.DB.InsertFeedback(fb)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库写入失败"})
		return
	}
	fb.ID = id

	go a.Mailer.SendFeedbackNotification(fb)
	go a.SendWebhookNotification(fb)

	c.JSON(http.StatusOK, gin.H{
		"message":        "反馈提交成功",
		"id":             fb.ID,
		"tracking_token": trackingToken,
	})
}

// allowedExtensions defines the file types accepted for upload.
var allowedExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".svg": true,
	".log": true, ".txt": true, ".csv": true, ".json": true,
}

// svgDangerousPatterns matches script tags and event handler attributes in SVG content.
var svgScriptTag = regexp.MustCompile(`(?i)<script[\s\S]*?</script>`)
var svgEventAttr = regexp.MustCompile(`(?i)\s+on\w+\s*=\s*"[^"]*"`)
var svgEventAttrSingle = regexp.MustCompile(`(?i)\s+on\w+\s*=\s*'[^']*'`)
var svgJavascriptURL = regexp.MustCompile(`(?i)(href|xlink:href)\s*=\s*["']?\s*javascript:`)

// sanitizeSVG removes dangerous elements and attributes from SVG content.
func sanitizeSVG(content string) string {
	// Remove <script>...</script> blocks
	content = svgScriptTag.ReplaceAllString(content, "")
	// Remove event handler attributes (onload, onerror, onclick, etc.)
	content = svgEventAttr.ReplaceAllString(content, "")
	content = svgEventAttrSingle.ReplaceAllString(content, "")
	// Remove javascript: URLs
	content = svgJavascriptURL.ReplaceAllString(content, `data-removed-href="`)
	return content
}

// validateFileContent checks file magic bytes to ensure content matches extension.
func validateFileContent(header []byte, ext string) bool {
	switch ext {
	case ".png":
		return len(header) >= 4 && header[0] == 0x89 && header[1] == 0x50 && header[2] == 0x4E && header[3] == 0x47
	case ".jpg", ".jpeg":
		return len(header) >= 2 && header[0] == 0xFF && header[1] == 0xD8
	case ".gif":
		return len(header) >= 3 && string(header[:3]) == "GIF"
	case ".webp":
		return len(header) >= 12 && string(header[8:12]) == "WEBP"
	case ".bmp":
		return len(header) >= 2 && header[0] == 0x42 && header[1] == 0x4D
	case ".svg":
		content := strings.TrimSpace(string(header))
		return strings.HasPrefix(content, "<") && (strings.Contains(content, "<svg") || strings.Contains(content, "<?xml"))
	case ".json":
		trimmed := bytes.TrimLeft(header, "\xef\xbb\xbf \t\r\n")
		return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
	case ".log", ".txt", ".csv":
		return true // text files — no reliable magic bytes
	default:
		return false
	}
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

	relDir := filepath.Join("uploads", projectID)
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

// ========== Admin Handlers ==========

func (a *App) AdminLogin(c *gin.Context) {
	// Block login if setup hasn't been completed yet
	if a.DB.GetConfig("setup_complete") != "true" {
		c.JSON(http.StatusForbidden, gin.H{"error": "请先完成初始设置"})
		return
	}

	clientIP := middleware.GetClientIP(c)

	// Brute force protection
	if a.LoginTracker.IsLocked(clientIP) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": "登录尝试次数过多，请 15 分钟后再试",
		})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	// Try admins table first
	var role string
	var authenticated bool
	admin, err := a.DB.GetAdminByUsername(req.Username)
	if err == nil && admin != nil && admin.IsActive {
		if checkPassword(req.Password, admin.PasswordHash) {
			role = admin.Role
			authenticated = true
		}
	}

	// Fallback to legacy config-based admin
	if !authenticated {
		dbUser := a.DB.GetConfig("admin_username")
		dbPwd := a.DB.GetConfig("admin_password")
		effectiveUser := a.Cfg.AdminUsername
		effectivePwd := a.Cfg.AdminPassword
		if dbUser != "" {
			effectiveUser = dbUser
		}
		if dbPwd != "" {
			effectivePwd = dbPwd
		}
		if middleware.SecureCompare(req.Username, effectiveUser) && checkPassword(req.Password, effectivePwd) {
			role = "admin"
			authenticated = true
		}
	}

	if !authenticated {
		a.LoginTracker.RecordFailure(clientIP)
		remaining := 10 - a.LoginTracker.FailureCount(clientIP)
		if remaining < 0 {
			remaining = 0
		}
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":     "用户名或密码错误",
			"remaining": remaining,
		})
		return
	}

	// Clear brute force tracker on success
	a.LoginTracker.ClearFailures(clientIP)

	token := a.SM.Create(req.Username, role)
	csrfToken := a.SM.GetCSRFToken(token)
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("admin_session", token, 86400, "/", "", a.cookieSecure(c), true)
	middleware.SetCSRFCookie(c, csrfToken, a.cookieSecure(c))

	a.DB.InsertAuditLog("login", "管理员登录", req.Username, clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "登录成功", "role": role})
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
	return strings.HasPrefix(a.Cfg.BaseURL, "https")
}

func (a *App) AdminLogout(c *gin.Context) {
	token, _ := c.Get("session_token")
	if t, ok := token.(string); ok {
		a.SM.Revoke(t)
	}
	c.SetCookie("admin_session", "", -1, "/", "", a.cookieSecure(c), true)
	c.SetCookie("csrf_token", "", -1, "/", "", false, false)
	c.JSON(http.StatusOK, gin.H{"message": "已退出"})
}

// AdminGetCSRFToken returns the CSRF token for the current session.
func (a *App) AdminGetCSRFToken(c *gin.Context) {
	token, _ := c.Get("session_token")
	t, ok := token.(string)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	csrfToken := a.SM.GetCSRFToken(t)
	c.JSON(http.StatusOK, gin.H{"csrf_token": csrfToken})
}

func (a *App) AdminStats(c *gin.Context) {
	total, projects, today, err := a.DB.GetStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取统计失败"})
		return
	}
	csatAvg, csatTotal, _, cerr := a.DB.GetCSATStats()
	if cerr != nil {
		csatAvg, csatTotal = 0, 0
	}
	c.JSON(http.StatusOK, gin.H{
		"total_feedbacks": total,
		"total_projects":  projects,
		"today_feedbacks": today,
		"csat_avg":        csatAvg,
		"csat_total":      csatTotal,
	})
}

func (a *App) AdminListFeedbacks(c *gin.Context) {
	project := c.Query("project")
	keyword := c.Query("keyword")
	status := c.Query("status")
	priority := c.Query("priority")
	assignee := c.Query("assignee")
	category := c.Query("category")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var projectIDs []string
	var accessPlan []database.ProjectAccess

	// Apply member_grants restrictions for non-admin roles
	username, _ := c.Get("admin_user")
	role, _ := c.Get("admin_role")
	roleStr, _ := role.(string)

	if roleStr != "admin" {
		if usernameStr, ok := username.(string); ok {
			admin, _ := a.DB.GetAdminByUsername(usernameStr)
			if admin != nil {
				plan, _ := a.DB.GetAdminAccessPlan(admin.ID)
				if plan != nil {
					// Empty plan = no grants = no access
					if len(plan) == 0 {
						c.JSON(http.StatusOK, gin.H{"feedbacks": []database.Feedback{}, "total": 0, "assignees": []string{}})
						return
					}
					// Check if any project has category restrictions
					hasCategoryRestriction := false
					for _, pa := range plan {
						if pa.AllowedCategories != nil {
							hasCategoryRestriction = true
							break
						}
					}

					if hasCategoryRestriction {
						// Use access plan for fine-grained filtering
						if project != "" {
							// Intersect: user-specified project must be in allowed list
							found := false
							for _, pa := range plan {
								if pa.Slug == project {
									found = true
									// Filter plan to only this project
									accessPlan = []database.ProjectAccess{pa}
									break
								}
							}
							if !found {
								c.JSON(http.StatusOK, gin.H{"feedbacks": []database.Feedback{}, "total": 0, "assignees": []string{}})
								return
							}
						} else {
							accessPlan = plan
						}
					} else {
						// All projects have wildcard — use simple project filter
						if project != "" {
							found := false
							for _, pa := range plan {
								if pa.Slug == project {
									found = true
									break
								}
							}
							if !found {
								c.JSON(http.StatusOK, gin.H{"feedbacks": []database.Feedback{}, "total": 0, "assignees": []string{}})
								return
							}
							projectIDs = []string{project}
						} else {
							for _, pa := range plan {
								projectIDs = append(projectIDs, pa.Slug)
							}
						}
					}
				}
			}
		}
	} else {
		// Admin role: use query param if specified
		if project != "" {
			projectIDs = []string{project}
		}
	}

	var list []database.Feedback
	var total int
	var err error

	if keyword != "" || status != "" || priority != "" || assignee != "" || category != "" {
		list, total, err = a.DB.SearchFeedbacks(projectIDs, accessPlan, keyword, status, priority, assignee, category, limit, offset)
	} else {
		list, total, err = a.DB.ListFeedbacks(projectIDs, accessPlan, limit, offset)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}

	projList, _ := a.DB.GetProjects()
	assignees, _ := a.DB.GetAssignees()

	c.JSON(http.StatusOK, gin.H{
		"feedbacks": list,
		"total":     total,
		"projects":  projList,
		"assignees": assignees,
	})
}

func (a *App) AdminGetFeedback(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	fb, deny := a.checkFeedbackReadPerm(c, id)
	if deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}

	c.JSON(http.StatusOK, fb)
}

// AdminUpdateFeedbackStatus updates the status and tags of a feedback.
func (a *App) AdminUpdateFeedbackStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	var req struct {
		Status string `json:"status"`
		Tags   string `json:"tags"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	validStatuses := map[string]bool{"pending": true, "processing": true, "resolved": true, "closed": true}
	if req.Status != "" && !validStatuses[req.Status] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的状态值"})
		return
	}

	// Fetch feedback before update to detect actual changes and check permissions
	oldFb, deny := a.checkFeedbackWritePerm(c, id)
	if deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}
	if oldFb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}
	oldStatus := oldFb.Status
	statusChanged := req.Status != "" && req.Status != oldStatus

	if err := a.DB.UpdateFeedbackStatus(id, req.Status, req.Tags); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_status", fmt.Sprintf("反馈 #%d 状态更新为 %s", id, req.Status), fmt.Sprintf("%v", user), clientIP)

	// Notify submitter only when status actually changed
	if statusChanged && oldFb.ContactEmail != "" {
		statusLabels := map[string]string{"pending": "待处理", "processing": "处理中", "resolved": "已解决", "closed": "已关闭"}
		label := statusLabels[req.Status]
		if label == "" {
			label = req.Status
		}
		vars := map[string]string{
			"id":     fmt.Sprintf("%d", oldFb.ID),
			"title":  oldFb.Title,
			"status": label,
		}
		subject := email.BuildStatusChangeSubject(a.DB, vars)
		body := email.BuildStatusChangeBody(a.DB, vars)
		go a.Mailer.SendStatusChangeNotification(oldFb, subject, body)

		// M2 CSAT: invite submitter to rate once resolved
		if req.Status == "resolved" && oldFb.ContactEmail != "" {
			trackURL := a.Cfg.BaseURL + "/track#token=" + oldFb.TrackingToken
			go a.Mailer.SendCSATInvite(oldFb, trackURL)
		}
	}

	// Webhook notification
	go a.sendWebhookEvent("status_change", map[string]interface{}{
		"id":         oldFb.ID,
		"project_id": oldFb.ProjectID,
		"title":      oldFb.Title,
		"status":     req.Status,
		"operator":   fmt.Sprintf("%v", user),
	}, oldFb)

	c.JSON(http.StatusOK, gin.H{"message": "状态已更新"})
}

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

func (a *App) AdminDeleteFeedback(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	fb, err := a.DB.GetFeedback(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}

	// Check write permission
	if _, deny := a.checkFeedbackWritePerm(c, id); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}

	var paths []string
	json.Unmarshal([]byte(fb.FilePaths), &paths)
	var fileErrors []string
	for _, p := range paths {
		absPath := filepath.Join(a.Cfg.DataDir, filepath.FromSlash(p))
		if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
			fileErrors = append(fileErrors, fmt.Sprintf("%s: %v", filepath.Base(absPath), err))
		}
	}

	if err := a.DB.DeleteFeedback(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_feedback", fmt.Sprintf("删除反馈 #%d (%s)", id, fb.Title), fmt.Sprintf("%v", user), clientIP)

	msg := "已删除"
	if len(fileErrors) > 0 {
		msg += "，但部分文件清理失败: " + strings.Join(fileErrors, "; ")
	}
	c.JSON(http.StatusOK, gin.H{"message": msg})
}

// ========== Public Setup & Dashboard ==========

// SetupStatus handles GET /api/v1/setup/status
func (a *App) SetupStatus(c *gin.Context) {
	val := a.DB.GetConfig("setup_complete")
	c.JSON(http.StatusOK, gin.H{
		"setup_complete": val == "true",
		"pow_difficulty":  a.Cfg.PoWDifficulty,
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

// ========== Public Projects API ==========

// PublicListProjects returns active projects for the feedback form.
func (a *App) PublicListProjects(c *gin.Context) {
	projects, err := a.DB.ListProjects()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取项目列表失败"})
		return
	}
	// Only return active, non-archived projects
	active := make([]database.Project, 0)
	for _, p := range projects {
		if p.IsActive && !p.IsArchived {
			active = append(active, p)
		}
	}
	c.JSON(http.StatusOK, gin.H{"projects": active})
}

// ========== Admin: Project Management ==========

func (a *App) AdminListProjects(c *gin.Context) {
	archivedParam := c.Query("archived")

	var projects []database.Project
	var err error

	if archivedParam == "true" {
		projects, err = a.DB.ListProjectsByArchive(true)
	} else if archivedParam == "false" {
		projects, err = a.DB.ListProjectsByArchive(false)
	} else {
		projects, err = a.DB.ListProjects()
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取项目列表失败: " + err.Error()})
		return
	}

	// Apply project member restrictions for non-admin roles
	username, _ := c.Get("admin_user")
	role, _ := c.Get("admin_role")
	if usernameStr, ok := username.(string); ok && role.(string) != "admin" {
		admin, _ := a.DB.GetAdminByUsername(usernameStr)
		if admin != nil {
			allowedSlugs, _ := a.DB.GetAdminProjectSlugs(admin.ID, role.(string))
			if allowedSlugs != nil {
				allowedSet := make(map[string]bool)
				for _, s := range allowedSlugs {
					allowedSet[s] = true
				}
				filtered := make([]database.Project, 0)
				for _, p := range projects {
					if allowedSet[p.Slug] {
						filtered = append(filtered, p)
					}
				}
				projects = filtered
			}
		}
	}

	if projects == nil {
		projects = []database.Project{}
	}

	c.JSON(http.StatusOK, gin.H{"projects": projects})
}

func (a *App) AdminCreateProject(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		FormSchema  string `json:"form_schema"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.Name == "" || req.Slug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "名称和标识不能为空"})
		return
	}
	// Validate slug: lowercase, alphanumeric + hyphens only
	for _, ch := range req.Slug {
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
			c.JSON(http.StatusBadRequest, gin.H{"error": "标识只能包含小写字母、数字、连字符和下划线"})
			return
		}
	}

	formSchema := req.FormSchema
	if formSchema == "" {
		formSchema = "[]"
	}
	p := &database.Project{
		Name:        req.Name,
		Slug:        req.Slug,
		Description: req.Description,
		IsActive:    true,
		FormSchema:  formSchema,
	}
	id, err := a.DB.CreateProject(p)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			c.JSON(http.StatusConflict, gin.H{"error": "标识已被使用"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败: " + err.Error()})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("create_project", fmt.Sprintf("创建项目 %s (%s)", req.Name, req.Slug), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusCreated, gin.H{"message": "项目已创建", "id": id})
}

func (a *App) AdminUpdateProject(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	var req struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		IsActive    bool   `json:"is_active"`
		IsArchived  bool   `json:"is_archived"`
		FormSchema  string `json:"form_schema"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	// Get existing project to detect slug change
	existing, err := a.DB.GetProject(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return
	}

	// If slug changed, record old slug in history for redirect
	if req.Slug != "" && req.Slug != existing.Slug {
		a.DB.InsertSlugHistory(existing.Slug, req.Slug)
	}

	// If form_schema not provided, preserve existing
	formSchema := req.FormSchema
	if formSchema == "" {
		formSchema = existing.FormSchema
		if formSchema == "" {
			formSchema = "[]"
		}
	}

	p := &database.Project{
		ID:          id,
		Name:        req.Name,
		Slug:        req.Slug,
		Description: req.Description,
		IsActive:    req.IsActive,
		IsArchived:  req.IsArchived,
		FormSchema:  formSchema,
	}
	if err := a.DB.UpdateProject(p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "项目已更新"})
}

func (a *App) AdminDeleteProject(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	// Get project info before deletion for audit log and file cleanup
	project, err := a.DB.GetProject(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return
	}

	// Get all feedbacks for this project to clean up files
	feedbacks, err := a.DB.ExportFeedbacks(project.Slug)
	if err == nil {
		for _, fb := range feedbacks {
			var paths []string
			json.Unmarshal([]byte(fb.FilePaths), &paths)
			for _, p := range paths {
				absPath := filepath.Join(a.Cfg.DataDir, filepath.FromSlash(p))
				if removeErr := os.Remove(absPath); removeErr != nil && !os.IsNotExist(removeErr) {
					log.Printf("[WARN] Failed to clean up file %s: %v", absPath, removeErr)
				}
			}
		}
	}

	// Cascade delete: removes project + associated feedbacks
	if err := a.DB.DeleteProject(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_project", fmt.Sprintf("删除项目 %s (%s)", project.Name, project.Slug), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "项目及关联反馈已删除"})
}

// ========== Admin: Project Stats ==========

func (a *App) AdminProjectStats(c *gin.Context) {
	stats, err := a.DB.GetProjectStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取统计失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

// ========== Admin: CSV Export ==========

func (a *App) AdminExportCSV(c *gin.Context) {
	projectID := c.Query("project")
	feedbacks, err := a.DB.ExportFeedbacks(projectID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "导出失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("export", fmt.Sprintf("导出反馈 %d 条 (项目: %s)", len(feedbacks), projectID), fmt.Sprintf("%v", user), clientIP)

	// M12: support json / xlsx export formats
	switch c.Query("fmt") {
	case "json":
		a.exportJSON(c, projectID, feedbacks)
		return
	case "xlsx":
		a.exportXLSX(c, projectID, feedbacks)
		return
	default:
		a.exportCSV(c, projectID, feedbacks)
	}
}

func (a *App) exportCSV(c *gin.Context, projectID string, feedbacks []database.Feedback) {
	filename := "feedbacks"
	if projectID != "" {
		filename = "feedbacks_" + projectID
	}
	filename += "_" + time.Now().Format("20060102_150405") + ".csv"

	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "text/csv; charset=utf-8")

	w := csv.NewWriter(c.Writer)
	// Write BOM for Excel compatibility
	c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	w.Write([]string{"ID", "项目", "标题", "描述", "自定义字段", "附件", "状态", "标签", "指派", "联系人", "联系邮箱", "来源IP", "提交时间"})
	for _, fb := range feedbacks {
		w.Write([]string{
			strconv.FormatInt(fb.ID, 10),
			fb.ProjectID,
			fb.Title,
			fb.Description,
			fb.CustomData,
			fb.FilePaths,
			fb.Status,
			fb.Tags,
			fb.Assignee,
			fb.ContactName,
			fb.ContactEmail,
			fb.ClientIP,
			fb.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	w.Flush()
}

func (a *App) exportJSON(c *gin.Context, projectID string, feedbacks []database.Feedback) {
	filename := "feedbacks"
	if projectID != "" {
		filename = "feedbacks_" + projectID
	}
	filename += "_" + time.Now().Format("20060102_150405") + ".json"

	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(c.Writer).Encode(feedbacks); err != nil {
		log.Printf("[EXPORT] JSON encode failed: %v", err)
	}
}

func (a *App) exportXLSX(c *gin.Context, projectID string, feedbacks []database.Feedback) {
	filename := "feedbacks"
	if projectID != "" {
		filename = "feedbacks_" + projectID
	}
	filename += "_" + time.Now().Format("20060102_150405") + ".xlsx"

	f := excelize.NewFile()
	sheet := "Feedbacks"
	f.SetSheetName("Sheet1", sheet)
	headers := []string{"ID", "项目", "标题", "描述", "自定义字段", "附件", "状态", "标签", "指派", "联系人", "联系邮箱", "来源IP", "提交时间"}
	f.SetSheetRow(sheet, "A1", &headers)
	for i, fb := range feedbacks {
		row := []interface{}{
			fb.ID, fb.ProjectID, fb.Title, fb.Description, fb.CustomData, fb.FilePaths,
			fb.Status, fb.Tags, fb.Assignee, fb.ContactName, fb.ContactEmail, fb.ClientIP,
			fb.CreatedAt.Format("2006-01-02 15:04:05"),
		}
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		f.SetSheetRow(sheet, cell, &row)
	}

	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	if err := f.Write(c.Writer); err != nil {
		log.Printf("[EXPORT] XLSX write failed: %v", err)
	}
}

// ========== Admin: Audit Logs ==========

func (a *App) AdminListAuditLogs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	logs, total, err := a.DB.ListAuditLogs(limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询审计日志失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"logs":  logs,
		"total": total,
	})
}

// ========== Admin: Config Sections ==========

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
		"base_url":              a.Cfg.BaseURL,
		"pow_difficulty":        a.Cfg.PoWDifficulty,
		"rate_limit_per_hr":     a.Cfg.RateLimitPerHour,
		"max_upload_mb":         a.Cfg.MaxUploadSize / 1024 / 1024,
		"webhook_url":           webhookURL,
		"webhook_url_deprecated": true,
		"webhook_type":          webhookType,
		"archive_days":          archiveDays,
		"backup_retention_days": backupRetention,
		"cdn_provider":          cdnProvider,
		"trusted_proxies":       trustedProxies,
	})
}

// AdminUpdateSystemConfig updates system-level config.
func (a *App) AdminUpdateSystemConfig(c *gin.Context) {
	var req struct {
		BaseURL           string `json:"base_url"`
		PoWDifficulty     int    `json:"pow_difficulty"`
		RateLimit         int    `json:"rate_limit_per_hr"`
		WebhookURL        string `json:"webhook_url"`
		WebhookType       string `json:"webhook_type"`
		ArchiveDays       string `json:"archive_days"`
		BackupRetention   string `json:"backup_retention_days"`
		CDNProvider       string `json:"cdn_provider"`
		TrustedProxies    string `json:"trusted_proxies"`
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

// ========== Webhook Notification ==========

// detectWebhookPlatform returns the platform name based on the webhook URL.
func detectWebhookPlatform(url string) string {
	switch {
	case strings.Contains(url, "open.feishu.cn") || strings.Contains(url, "feishu"):
		return "feishu"
	case strings.Contains(url, "oapi.dingtalk.com"):
		return "dingtalk"
	case strings.Contains(url, "hooks.slack.com"):
		return "slack"
	case strings.Contains(url, "qyapi.weixin.qq.com"):
		return "wecom"
	default:
		return "generic"
	}
}

// SendWebhookNotification sends a new-feedback webhook notification.
func (a *App) SendWebhookNotification(fb *database.Feedback) {
	a.sendWebhookEvent("new_feedback", map[string]interface{}{
		"id":          fb.ID,
		"project_id":  fb.ProjectID,
		"title":       fb.Title,
		"description": fb.Description,
		"custom_data": fb.CustomData,
		"client_ip":   fb.ClientIP,
		"created_at":  fb.CreatedAt.Format(time.RFC3339),
	}, fb)
}

// sendWebhookEvent sends a webhook notification for any event type.
func (a *App) sendWebhookEvent(event string, data map[string]interface{}, fb *database.Feedback) {
	// Build the platform-specific payload the same way as before.
	webhookURL := a.DB.GetConfig("webhook_url")
	if webhookURL == "" {
		webhookURL = a.Cfg.WebhookURL
	}

	var payload []byte
	var err error
	var platform string

	if fb != nil {
		platform = a.DB.GetConfig("webhook_type")
		if platform == "" && webhookURL != "" {
			platform = detectWebhookPlatform(webhookURL)
		}
		switch platform {
		case "feishu":
			payload, err = buildFeishuCard(event, data, fb)
		case "dingtalk":
			payload, err = buildDingTalkCard(event, data, fb)
		case "slack":
			payload, err = buildSlackCard(event, data, fb)
		case "wecom":
			payload, err = buildWeComCard(event, data, fb)
		default:
			wrapper := map[string]interface{}{
				"event":     event,
				"data":      data,
				"timestamp": time.Now().Format(time.RFC3339),
			}
			payload, err = json.Marshal(wrapper)
		}
	} else {
		wrapper := map[string]interface{}{
			"event":     event,
			"data":      data,
			"timestamp": time.Now().Format(time.RFC3339),
		}
		payload, err = json.Marshal(wrapper)
	}
	if err != nil {
		log.Printf("[WEBHOOK] Failed to build %s payload for %s: %v", platform, event, err)
		return
	}

	slug := ""
	if fb != nil {
		slug = fb.ProjectID
	}

	// Enqueue for subscription-based delivery (with retry + signature at send time).
	// The legacy single webhook_url channel has been removed (app.go:1668); all
	// outbound webhooks now go through HMAC-signed subscription deliveries.
	if eerr := a.DB.EnqueueWebhook(event, string(payload), slug); eerr != nil {
		log.Printf("[WEBHOOK] Enqueue failed for %s: %v", event, eerr)
	}
}

// ProcessWebhookOutbox delivers due webhook outbox rows with HMAC signing and exponential backoff.
func (a *App) ProcessWebhookOutbox() {
	batch, err := a.DB.GetDueOutbox(time.Now().Unix(), 50)
	if err != nil {
		log.Printf("[WEBHOOK] outbox query failed: %v", err)
		return
	}
	for _, o := range batch {
		a.deliverWebhook(o)
	}
}

func (a *App) deliverWebhook(o database.WebhookOutbox) {
	req, err := http.NewRequest(http.MethodPost, o.URL, bytes.NewReader([]byte(o.Payload)))
	if err != nil {
		a.DB.MarkOutboxFailure(o.ID, err.Error(), o.Attempts+1, time.Now().Unix()+webhookBackoff(o.Attempts), 8)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if o.Secret != "" {
		mac := hmac.New(sha256.New, []byte(o.Secret))
		mac.Write([]byte(o.Payload))
		req.Header.Set("X-FeedShit-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		a.DB.MarkOutboxFailure(o.ID, err.Error(), o.Attempts+1, time.Now().Unix()+webhookBackoff(o.Attempts), 8)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		a.DB.MarkOutboxSuccess(o.ID)
		log.Printf("[WEBHOOK] delivered outbox #%d to %s", o.ID, o.URL)
	} else {
		a.DB.MarkOutboxFailure(o.ID, fmt.Sprintf("status %d: %s", resp.StatusCode, string(body)), o.Attempts+1, time.Now().Unix()+webhookBackoff(o.Attempts), 8)
	}
}

// webhookBackoff returns the retry delay (seconds) for a given attempt count, capped at 1h.
func webhookBackoff(attempts int) int64 {
	d := 30 * (1 << uint(attempts))
	if d > 3600 {
		d = 3600
	}
	return int64(d)
}

// ========== M6 Webhook Subscriptions (admin CRUD) ==========

func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}

func (a *App) AdminListWebhookSubscriptions(c *gin.Context) {
	subs, err := a.DB.ListWebhookSubscriptions()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if subs == nil {
		subs = []database.WebhookSubscription{}
	}
	for i := range subs {
		subs[i].Secret = maskSecret(subs[i].Secret)
	}
	c.JSON(http.StatusOK, gin.H{"subscriptions": subs})
}

func (a *App) AdminCreateWebhookSubscription(c *gin.Context) {
	var req struct {
		ProjectSlug string `json:"project_slug"`
		URL         string `json:"url"`
		Secret      string `json:"secret"`
		Events      string `json:"events"`
		IsActive    bool   `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "URL 不能为空"})
		return
	}
	if req.Events == "" {
		req.Events = "*"
	}
	id, err := a.DB.CreateWebhookSubscription(req.ProjectSlug, req.URL, req.Secret, req.Events, req.IsActive)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("create_webhook_sub", fmt.Sprintf("创建 Webhook 订阅 #%d", id), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusCreated, gin.H{"id": id, "message": "已创建"})
}

func (a *App) AdminUpdateWebhookSubscription(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	var req struct {
		URL      string `json:"url"`
		Secret   string `json:"secret"`
		Events   string `json:"events"`
		IsActive *bool  `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	// Preserve existing secret when not provided
	if req.Secret == "" {
		subs, _ := a.DB.ListWebhookSubscriptions()
		for _, s := range subs {
			if s.ID == id {
				req.Secret = s.Secret
				break
			}
		}
	}
	if err := a.DB.UpdateWebhookSubscription(id, req.URL, req.Secret, req.Events, req.IsActive); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_webhook_sub", fmt.Sprintf("更新 Webhook 订阅 #%d", id), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

func (a *App) AdminDeleteWebhookSubscription(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	if err := a.DB.DeleteWebhookSubscription(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_webhook_sub", fmt.Sprintf("删除 Webhook 订阅 #%d", id), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// eventTitle returns a human-readable title for a webhook event.
func eventTitle(event string) string {
	switch event {
	case "new_feedback":
		return "新反馈"
	case "status_change":
		return "状态变更"
	case "new_note":
		return "新增备注"
	case "priority_change":
		return "优先级变更"
	case "assignee_change":
		return "指派变更"
	default:
		return event
	}
}

// buildFeishuCard builds a Feishu interactive card message.
func buildFeishuCard(event string, data map[string]interface{}, fb *database.Feedback) ([]byte, error) {
	title := fmt.Sprintf("%s - %v", eventTitle(event), data["title"])
	id := data["id"]
	card := map[string]interface{}{
		"msg_type": "interactive",
		"card": map[string]interface{}{
			"header": map[string]interface{}{
				"title": map[string]string{
					"tag":     "plain_text",
					"content": title,
				},
				"template": "blue",
			},
			"elements": []map[string]interface{}{
				{
					"tag": "div",
					"fields": []map[string]interface{}{
						{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**项目：** %v", data["project_id"])}},
						{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**编号：** #%v", id)}},
					},
				},
			},
		},
	}

	// Add description if available
	if desc, ok := data["description"].(string); ok && desc != "" {
		if len(desc) > 200 {
			desc = desc[:200] + "..."
		}
		elements := card["card"].(map[string]interface{})["elements"].([]map[string]interface{})
		elements = append(elements, map[string]interface{}{
			"tag": "div",
			"text": map[string]string{
				"tag":     "lark_md",
				"content": html.EscapeString(desc),
			},
		})
		card["card"].(map[string]interface{})["elements"] = elements
	}

	// Add status/priority if from feedback
	if fb != nil {
		elements := card["card"].(map[string]interface{})["elements"].([]map[string]interface{})
		statusLabels := map[string]string{"pending": "待处理", "processing": "处理中", "resolved": "已解决", "closed": "已关闭"}
		priorityLabels := map[string]string{"urgent": "🔴 紧急", "high": "🟡 高", "medium": "🔵 中", "low": "⚪ 低"}
		fields := []map[string]interface{}{
			{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**状态：** %s", statusLabels[fb.Status])}},
		}
		if fb.Priority != "" {
			fields = append(fields, map[string]interface{}{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**优先级：** %s", priorityLabels[fb.Priority])}})
		}
		elements = append(elements, map[string]interface{}{"tag": "div", "fields": fields})
		card["card"].(map[string]interface{})["elements"] = elements
	}

	return json.Marshal(card)
}

// buildDingTalkCard builds a DingTalk markdown message.
func buildDingTalkCard(event string, data map[string]interface{}, fb *database.Feedback) ([]byte, error) {
	title := fmt.Sprintf("%s - %v", eventTitle(event), data["title"])
	var md strings.Builder
	md.WriteString(fmt.Sprintf("### %s\n\n", title))
	md.WriteString(fmt.Sprintf("- **编号：** #%v\n", data["id"]))
	md.WriteString(fmt.Sprintf("- **项目：** %v\n", data["project_id"]))
	if desc, ok := data["description"].(string); ok && desc != "" {
		if len(desc) > 200 {
			desc = desc[:200] + "..."
		}
		md.WriteString(fmt.Sprintf("- **描述：** %s\n", desc))
	}
	if fb != nil {
		statusLabels := map[string]string{"pending": "待处理", "processing": "处理中", "resolved": "已解决", "closed": "已关闭"}
		md.WriteString(fmt.Sprintf("- **状态：** %s\n", statusLabels[fb.Status]))
		if fb.Priority != "" {
			priorityLabels := map[string]string{"urgent": "紧急", "high": "高", "medium": "中", "low": "低"}
			md.WriteString(fmt.Sprintf("- **优先级：** %s\n", priorityLabels[fb.Priority]))
		}
	}

	payload := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"title": title,
			"text":  md.String(),
		},
	}
	return json.Marshal(payload)
}

// buildSlackCard builds a Slack Block Kit message.
func buildSlackCard(event string, data map[string]interface{}, fb *database.Feedback) ([]byte, error) {
	title := fmt.Sprintf("%s - %v", eventTitle(event), data["title"])
	blocks := []map[string]interface{}{
		{
			"type": "header",
			"text": map[string]string{"type": "plain_text", "text": title},
		},
		{
			"type":   "section",
			"fields": []map[string]interface{}{
				{"type": "mrkdwn", "text": fmt.Sprintf("*编号:*\n#%v", data["id"])},
				{"type": "mrkdwn", "text": fmt.Sprintf("*项目:*\n%v", data["project_id"])},
			},
		},
	}

	if desc, ok := data["description"].(string); ok && desc != "" {
		if len(desc) > 500 {
			desc = desc[:500] + "..."
		}
		blocks = append(blocks, map[string]interface{}{
			"type": "section",
			"text": map[string]string{"type": "mrkdwn", "text": desc},
		})
	}

	if fb != nil {
		statusLabels := map[string]string{"pending": "待处理", "processing": "处理中", "resolved": "已解决", "closed": "已关闭"}
		fields := []map[string]interface{}{
			{"type": "mrkdwn", "text": fmt.Sprintf("*状态:*\n%s", statusLabels[fb.Status])},
		}
		if fb.Priority != "" {
			priorityLabels := map[string]string{"urgent": "🔴 紧急", "high": "🟡 高", "medium": "🔵 中", "low": "⚪ 低"}
			fields = append(fields, map[string]interface{}{"type": "mrkdwn", "text": fmt.Sprintf("*优先级:*\n%s", priorityLabels[fb.Priority])})
		}
		blocks = append(blocks, map[string]interface{}{"type": "section", "fields": fields})
	}

	payload := map[string]interface{}{
		"text":   title,
		"blocks": blocks,
	}
	return json.Marshal(payload)
}

// buildWeComCard builds a WeCom (企业微信) markdown message.
func buildWeComCard(event string, data map[string]interface{}, fb *database.Feedback) ([]byte, error) {
	title := fmt.Sprintf("%s - %v", eventTitle(event), data["title"])
	var md strings.Builder
	md.WriteString(fmt.Sprintf("## %s\n\n", title))
	md.WriteString(fmt.Sprintf("> 编号: **#%v**\n", data["id"]))
	md.WriteString(fmt.Sprintf("> 项目: **%v**\n", data["project_id"]))
	if desc, ok := data["description"].(string); ok && desc != "" {
		if len(desc) > 200 {
			desc = desc[:200] + "..."
		}
		md.WriteString(fmt.Sprintf("> 描述: %s\n", desc))
	}
	if fb != nil {
		statusLabels := map[string]string{"pending": "待处理", "processing": "处理中", "resolved": "已解决", "closed": "已关闭"}
		md.WriteString(fmt.Sprintf("> 状态: <font color=\"info\">%s</font>\n", statusLabels[fb.Status]))
		if fb.Priority != "" {
			priorityLabels := map[string]string{"urgent": "紧急", "high": "高", "medium": "中", "low": "低"}
			md.WriteString(fmt.Sprintf("> 优先级: %s\n", priorityLabels[fb.Priority]))
		}
	}

	payload := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": md.String(),
		},
	}
	return json.Marshal(payload)
}

// ========== Feedback Notes (Replies / Internal Notes) ==========

func (a *App) AdminAddFeedbackNote(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	// Verify feedback exists
	fb, err := a.DB.GetFeedback(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}

	// Check write permission
	if _, deny := a.checkFeedbackWritePerm(c, id); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}

	var req struct {
		Content  string `json:"content"`
		IsPublic bool   `json:"is_public"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "内容不能为空"})
		return
	}

	user, _ := c.Get("admin_user")
	author := fmt.Sprintf("%v", user)

	noteID, err := a.DB.InsertFeedbackNote(id, req.Content, author, req.IsPublic)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}

	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("add_note", fmt.Sprintf("反馈 #%d 添加备注", id), author, clientIP)

	// Notify submitter when a public reply is added
	if req.IsPublic && fb.ContactEmail != "" {
		vars := map[string]string{
			"id":           fmt.Sprintf("%d", fb.ID),
			"title":        fb.Title,
			"note_content": req.Content,
			"author":       author,
		}
		subject := email.BuildReplySubject(a.DB, vars)
		body := email.BuildReplyBody(a.DB, vars)
		go a.Mailer.SendStatusChangeNotification(fb, subject, body)
	}

	// Webhook notification for new note
	go a.sendWebhookEvent("new_note", map[string]interface{}{
		"id":         fb.ID,
		"project_id": fb.ProjectID,
		"title":      fb.Title,
		"note":       req.Content,
		"is_public":  req.IsPublic,
		"author":     author,
	}, fb)

	c.JSON(http.StatusCreated, gin.H{"message": "备注已添加", "id": noteID})
}

func (a *App) AdminListFeedbackNotes(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	// Authorization: only users with read access to the feedback may list its notes.
	fb, deny := a.checkFeedbackReadPerm(c, id)
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}
	if deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}

	notes, err := a.DB.ListFeedbackNotes(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if notes == nil {
		notes = []database.FeedbackNote{}
	}
	c.JSON(http.StatusOK, gin.H{"notes": notes})
}

func (a *App) AdminDeleteFeedbackNote(c *gin.Context) {
	noteID, err := strconv.ParseInt(c.Param("noteId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	// Resolve the note to find its parent feedback, then enforce write permission.
	note, err := a.DB.GetFeedbackNote(noteID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if note == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "备注不存在"})
		return
	}

	fb, deny := a.checkFeedbackWritePerm(c, note.FeedbackID)
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}
	if deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}

	if err := a.DB.DeleteFeedbackNote(noteID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_note", fmt.Sprintf("删除备注 #%d", noteID), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "备注已删除"})
}

// ========== Feedback Assignee ==========

func (a *App) AdminUpdateFeedbackAssignee(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	// Check write permission
	if _, deny := a.checkFeedbackWritePerm(c, id); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}

	var req struct {
		Assignee string `json:"assignee"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	if err := a.DB.UpdateFeedbackAssignee(id, req.Assignee); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("assign_feedback", fmt.Sprintf("反馈 #%d 指派给 %s", id, req.Assignee), fmt.Sprintf("%v", user), clientIP)

	// Webhook notification for assignee change
	fb, _ := a.DB.GetFeedback(id)
	if fb != nil {
		go a.sendWebhookEvent("assignee_change", map[string]interface{}{
			"id":         fb.ID,
			"project_id": fb.ProjectID,
			"title":      fb.Title,
			"assignee":   req.Assignee,
			"operator":   fmt.Sprintf("%v", user),
		}, fb)
	}

	c.JSON(http.StatusOK, gin.H{"message": "指派已更新"})
}

// ========== Bulk Operations ==========

func (a *App) AdminBulkDeleteFeedbacks(c *gin.Context) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要删除的反馈"})
		return
	}
	if len(req.IDs) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "单次最多操作 500 条"})
		return
	}

	// Clean up files for all feedbacks being deleted
	for _, id := range req.IDs {
		fb, err := a.DB.GetFeedback(id)
		if err != nil {
			continue
		}
		var paths []string
		json.Unmarshal([]byte(fb.FilePaths), &paths)
		for _, p := range paths {
			absPath := filepath.Join(a.Cfg.DataDir, filepath.FromSlash(p))
			os.Remove(absPath)
		}
	}

	affected, err := a.DB.BulkDeleteFeedbacks(req.IDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "批量删除失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("bulk_delete", fmt.Sprintf("批量删除 %d 条反馈", affected), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已删除 %d 条反馈", affected), "affected": affected})
}

func (a *App) AdminBulkUpdateStatus(c *gin.Context) {
	var req struct {
		IDs    []int64 `json:"ids"`
		Status string  `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要更新的反馈"})
		return
	}
	if len(req.IDs) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "单次最多操作 500 条"})
		return
	}

	validStatuses := map[string]bool{"pending": true, "processing": true, "resolved": true, "closed": true}
	if !validStatuses[req.Status] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的状态值"})
		return
	}

	affected, err := a.DB.BulkUpdateFeedbackStatus(req.IDs, req.Status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "批量更新失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("bulk_status", fmt.Sprintf("批量更新 %d 条反馈状态为 %s", affected, req.Status), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已更新 %d 条反馈状态", affected), "affected": affected})
}

// ========== Chart Data ==========

func (a *App) AdminChartData(c *gin.Context) {
	daysStr := c.DefaultQuery("days", "30")
	days, _ := strconv.Atoi(daysStr)
	if days <= 0 || days > 365 {
		days = 30
	}

	trend, err := a.DB.GetDailyTrend(days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取趋势数据失败"})
		return
	}
	if trend == nil {
		trend = []map[string]interface{}{}
	}

	statusDist, err := a.DB.GetStatusDistribution()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取状态分布失败"})
		return
	}
	if statusDist == nil {
		statusDist = []map[string]interface{}{}
	}

	catDist, err := a.DB.GetCategoryCounts("")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取分类分布失败"})
		return
	}
	if catDist == nil {
		catDist = map[string]int{}
	}

	c.JSON(http.StatusOK, gin.H{
		"daily_trend":          trend,
		"status_distribution":  statusDist,
		"category_distribution": catDist,
	})
}

// ========== Backup ==========

func (a *App) AdminBackup(c *gin.Context) {
	backupDir := filepath.Join(a.Cfg.DataDir, "backups")
	backupPath, err := a.DB.BackupDatabase(backupDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "备份失败: " + err.Error()})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("backup", fmt.Sprintf("数据库备份: %s", filepath.Base(backupPath)), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{
		"message": "备份完成",
		"path":    filepath.Base(backupPath),
	})
}

// ========== Public Tracking (Submitter Self-Service) ==========

// PublicTrackFeedback returns feedback details by tracking token.
// Only returns public-safe fields (no IP, no internal notes).
func (a *App) PublicTrackFeedback(c *gin.Context) {
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少跟踪令牌"})
		return
	}

	fb, err := a.DB.GetFeedbackByTrackingToken(token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到对应的反馈记录"})
		return
	}

	// Get public notes only
	notes, err := a.DB.ListFeedbackNotes(fb.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询备注失败"})
		return
	}
	var publicNotes []database.FeedbackNote
	for _, n := range notes {
		if n.IsPublic {
			publicNotes = append(publicNotes, n)
		}
	}
	if publicNotes == nil {
		publicNotes = []database.FeedbackNote{}
	}

	rating, _ := a.DB.GetRating(fb.ID)
	resp := gin.H{
		"id":           fb.ID,
		"project_id":   fb.ProjectID,
		"title":        fb.Title,
		"description":  fb.Description,
		"status":       fb.Status,
		"category":     fb.Category,
		"priority":     fb.Priority,
		"votes":        fb.Votes,
		"created_at":   fb.CreatedAt.Format("2006-01-02 15:04:05"),
		"allow_rating": fb.Status == "resolved",
		"notes":        publicNotes,
	}
	if rating != nil {
		resp["rating"] = rating.Score
		resp["rating_comment"] = rating.Comment
	}

	c.JSON(http.StatusOK, resp)
}

// PublicSubmitReply allows a submitter to add a follow-up reply to their feedback.
func (a *App) PublicSubmitReply(c *gin.Context) {
	token := strings.TrimSpace(c.PostForm("token"))
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少跟踪令牌"})
		return
	}

	fb, err := a.DB.GetFeedbackByTrackingToken(token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到对应的反馈记录"})
		return
	}

	content := strings.TrimSpace(c.PostForm("content"))
	if content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "回复内容不能为空"})
		return
	}
	if len(content) > 2000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "回复内容最多 2000 字"})
		return
	}

	noteID, err := a.DB.InsertSubmitterReply(fb.ID, content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存回复失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "回复已提交",
		"note_id": noteID,
	})
}

// PublicSubmitRating lets a submitter rate a resolved feedback via their tracking token (M2 CSAT).
func (a *App) PublicSubmitRating(c *gin.Context) {
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少跟踪令牌"})
		return
	}
	fb, err := a.DB.GetFeedbackByTrackingToken(token)
	if err != nil || fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到对应的反馈记录"})
		return
	}
	if fb.Status != "resolved" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅已解决的反馈可评分"})
		return
	}

	var req struct {
		Score   int    `json:"score"`
		Comment string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.Score < 1 || req.Score > 5 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "评分必须为 1-5"})
		return
	}

	if err := a.DB.UpsertRating(fb.ID, req.Score, strings.TrimSpace(req.Comment)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存评分失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("csat_rating", fmt.Sprintf("反馈 #%d 评分 %d", fb.ID, req.Score), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "评分已提交", "score": req.Score})
}

// ========== Current User ==========

// AdminGetCurrentUser returns the currently logged-in user's info.
func (a *App) AdminGetCurrentUser(c *gin.Context) {
	user, _ := c.Get("admin_user")
	role, _ := c.Get("admin_role")
	c.JSON(http.StatusOK, gin.H{
		"username": user,
		"role":     role,
	})
}

// ========== Admin Team Management ==========

// AdminListAdmins returns all admin accounts.
func (a *App) AdminListAdmins(c *gin.Context) {
	admins, err := a.DB.ListAdmins()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if admins == nil {
		admins = []database.Admin{}
	}
	c.JSON(http.StatusOK, gin.H{"admins": admins})
}

// AdminCreateAdmin creates a new admin account.
func (a *App) AdminCreateAdmin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		Grants   []struct {
			ProjectSlug string `json:"project_slug"`
			CategoryKey string `json:"category_key"`
			Role        string `json:"role"`
		} `json:"grants"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名不能为空"})
		return
	}
	if len(req.Username) < 3 || len(req.Username) > 32 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名长度 3-32 位"})
		return
	}
	if req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码不能为空"})
		return
	}
	if err := validatePasswordStrength(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	validRoles := map[string]bool{"admin": true, "manager": true, "editor": true, "viewer": true}
	if !validRoles[req.Role] {
		req.Role = "editor"
	}

	// Validate initial grants before creating the account (fail fast)
	var grants []database.MemberGrant
	if len(req.Grants) > 0 {
		grantRoles := map[string]bool{"viewer": true, "editor": true, "manager": true}
		grants = make([]database.MemberGrant, 0, len(req.Grants))
		for i, g := range req.Grants {
			if g.ProjectSlug == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("第 %d 条授权缺少 project_slug", i+1)})
				return
			}
			if g.CategoryKey == "" {
				g.CategoryKey = "*"
			}
			if !grantRoles[g.Role] {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("第 %d 条授权角色无效: %s", i+1, g.Role)})
				return
			}
			proj, perr := a.DB.GetProjectBySlug(g.ProjectSlug)
			if perr != nil || proj == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("项目不存在: %s", g.ProjectSlug)})
				return
			}
			if g.CategoryKey != "*" {
				cat, cerr := a.DB.GetCategoryByKey(g.ProjectSlug, g.CategoryKey)
				if cerr != nil || cat == nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("第 %d 条授权分类不存在: %s", i+1, g.CategoryKey)})
					return
				}
			}
			grants = append(grants, database.MemberGrant{ProjectSlug: g.ProjectSlug, CategoryKey: g.CategoryKey, Role: g.Role})
		}
	}

	// Check if username already exists
	existing, _ := a.DB.GetAdminByUsername(req.Username)
	if existing != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "用户名已存在"})
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}

	id, err := a.DB.CreateAdmin(req.Username, hash, req.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
		return
	}

	// Persist initial grants so the new admin isn't empty-handed
	if len(grants) > 0 {
		if gerr := a.DB.SetMemberGrants(id, grants); gerr != nil {
			log.Printf("[ADMIN] failed to set initial grants for %s: %v", req.Username, gerr)
		}
	}

	currentUser, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("create_admin", fmt.Sprintf("创建管理员 %s (角色: %s, 授权 %d 条)", req.Username, req.Role, len(grants)), fmt.Sprintf("%v", currentUser), clientIP)

	c.JSON(http.StatusCreated, gin.H{"message": "管理员已创建", "id": id})
}

// AdminUpdateAdmin updates an existing admin's role, active status, or password.
func (a *App) AdminUpdateAdmin(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	var req struct {
		Role     string `json:"role"`
		IsActive *bool  `json:"is_active"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	admin, err := a.DB.GetAdminByID(id)
	if err != nil || admin == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "管理员不存在"})
		return
	}

	// Prevent self-deactivation
	currentUser, _ := c.Get("admin_user")
	if currentUser == admin.Username && req.IsActive != nil && !*req.IsActive {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能停用自己的账号"})
		return
	}

	validRoles := map[string]bool{"admin": true, "manager": true, "editor": true, "viewer": true}
	if !validRoles[req.Role] {
		req.Role = admin.Role
	}

	isActive := admin.IsActive
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	var pwdHash string
	if req.Password != "" {
		if err := validatePasswordStrength(req.Password); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		pwdHash, err = hashPassword(req.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
			return
		}
	}

	if err := a.DB.UpdateAdmin(id, req.Role, isActive, pwdHash); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}

	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_admin", fmt.Sprintf("更新管理员 %s", admin.Username), fmt.Sprintf("%v", currentUser), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "管理员已更新"})
}

// AdminDeleteAdmin deletes an admin account.
func (a *App) AdminDeleteAdmin(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	admin, err := a.DB.GetAdminByID(id)
	if err != nil || admin == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "管理员不存在"})
		return
	}

	// Prevent self-deletion
	currentUser, _ := c.Get("admin_user")
	if currentUser == admin.Username {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除自己的账号"})
		return
	}

	if err := a.DB.DeleteAdmin(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}

	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_admin", fmt.Sprintf("删除管理员 %s", admin.Username), fmt.Sprintf("%v", currentUser), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "管理员已删除"})
}

// ========== Member Grants (Fine-grained RBAC) ==========

// AdminGetMemberGrants returns all grants for a specific admin.
func (a *App) AdminGetMemberGrants(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	admin, err := a.DB.GetAdminByID(id)
	if err != nil || admin == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "管理员不存在"})
		return
	}

	grants, err := a.DB.ListMemberGrants(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if grants == nil {
		grants = []database.MemberGrant{}
	}
	c.JSON(http.StatusOK, gin.H{"grants": grants})
}

// AdminSetMemberGrants replaces all grants for an admin with the provided list.
func (a *App) AdminSetMemberGrants(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	admin, err := a.DB.GetAdminByID(id)
	if err != nil || admin == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "管理员不存在"})
		return
	}

	var req struct {
		Grants []struct {
			ProjectSlug string `json:"project_slug"`
			CategoryKey string `json:"category_key"`
			Role        string `json:"role"`
		} `json:"grants"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	validRoles := map[string]bool{"viewer": true, "editor": true, "manager": true}
	grants := make([]database.MemberGrant, 0, len(req.Grants))
	for i, g := range req.Grants {
		if g.ProjectSlug == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("第 %d 条授权缺少 project_slug", i+1)})
			return
		}
		if g.CategoryKey == "" {
			g.CategoryKey = "*"
		}
		if !validRoles[g.Role] {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("第 %d 条授权角色无效: %s", i+1, g.Role)})
			return
		}
		// Verify project exists
		proj, err := a.DB.GetProjectBySlug(g.ProjectSlug)
		if err != nil || proj == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("项目不存在: %s", g.ProjectSlug)})
			return
		}
		// Verify category_key exists in project dictionary (wildcard "*" is always allowed)
		if g.CategoryKey != "*" {
			cat, catErr := a.DB.GetCategoryByKey(g.ProjectSlug, g.CategoryKey)
			if catErr != nil || cat == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("第 %d 条授权分类不存在: %s", i+1, g.CategoryKey)})
				return
			}
		}
		grants = append(grants, database.MemberGrant{
			ProjectSlug: g.ProjectSlug,
			CategoryKey: g.CategoryKey,
			Role:        g.Role,
		})
	}

	if err := a.DB.SetMemberGrants(id, grants); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "设置失败"})
		return
	}

	currentUser, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("set_member_grants", fmt.Sprintf("设置 %s 的授权 (%d 条)", admin.Username, len(grants)), fmt.Sprintf("%v", currentUser), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "授权已更新", "count": len(grants)})
}

// AdminDeleteMemberGrant removes a single grant by ID.
func (a *App) AdminDeleteMemberGrant(c *gin.Context) {
	grantID, err := strconv.ParseInt(c.Param("grantId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的授权 ID"})
		return
	}

	if err := a.DB.DeleteMemberGrant(grantID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}

	currentUser, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_member_grant", fmt.Sprintf("删除授权 #%d", grantID), fmt.Sprintf("%v", currentUser), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "授权已撤销"})
}

// ========== Priority & Duplicate ==========

// AdminUpdateFeedbackPriority updates the priority of a feedback.
func (a *App) AdminUpdateFeedbackPriority(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	// Check write permission
	if _, deny := a.checkFeedbackWritePerm(c, id); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}

	var req struct {
		Priority string `json:"priority"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	validPriorities := map[string]bool{"": true, "low": true, "medium": true, "high": true, "urgent": true}
	if !validPriorities[req.Priority] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的优先级"})
		return
	}

	if err := a.DB.UpdateFeedbackPriority(id, req.Priority); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("set_priority", fmt.Sprintf("反馈 #%d 优先级设为 %s", id, req.Priority), fmt.Sprintf("%v", user), clientIP)

	// Webhook notification for priority change
	fb, _ := a.DB.GetFeedback(id)
	if fb != nil {
		go a.sendWebhookEvent("priority_change", map[string]interface{}{
			"id":         fb.ID,
			"project_id": fb.ProjectID,
			"title":      fb.Title,
			"priority":   req.Priority,
			"operator":   fmt.Sprintf("%v", user),
		}, fb)
	}

	c.JSON(http.StatusOK, gin.H{"message": "优先级已更新"})
}

// AdminMarkAsDuplicate marks a feedback as a duplicate of another.
func (a *App) AdminMarkAsDuplicate(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	var req struct {
		DuplicateOf int64 `json:"duplicate_of"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	if req.DuplicateOf <= 0 || req.DuplicateOf == id {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的目标反馈 ID"})
		return
	}

	// Check write permission (also loads the feedback)
	fb, deny := a.checkFeedbackWritePerm(c, id)
	if deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}
	// Cross-project guard: target must belong to the same project.
	target, tErr := a.DB.GetFeedback(req.DuplicateOf)
	if tErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "目标反馈不存在"})
		return
	}
	if fb.ProjectID != target.ProjectID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能跨项目合并"})
		return
	}

	if err := a.DB.MarkAsDuplicate(id, req.DuplicateOf); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "标记失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("mark_duplicate", fmt.Sprintf("反馈 #%d 标记为 #%d 的重复", id, req.DuplicateOf), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "已标记为重复"})
}

// AdminUnmarkDuplicate clears the duplicate flag on a feedback.
func (a *App) AdminUnmarkDuplicate(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	if err := a.DB.UnmarkDuplicate(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("unmark_duplicate", fmt.Sprintf("反馈 #%d 取消重复标记", id), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "已取消重复标记"})
}

// ========== API Token Management ==========

// AdminListAPITokens returns all API tokens.
func (a *App) AdminListAPITokens(c *gin.Context) {
	tokens, err := a.DB.ListAPITokens()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取失败"})
		return
	}
	if tokens == nil {
		tokens = []database.APIToken{}
	}
	c.JSON(http.StatusOK, gin.H{"tokens": tokens})
}

// AdminCreateAPIToken creates a new API token.
func (a *App) AdminCreateAPIToken(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		ProjectID   string `json:"project_id"`
		RateLimit   int    `json:"rate_limit"`
		QuotaPerDay int    `json:"quota_per_day"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "名称不能为空"})
		return
	}

	// Generate a random token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成令牌失败"})
		return
	}
	tokenStr := "fs_" + hex.EncodeToString(tokenBytes)

	// Apply the configured default rate limit when the caller does not specify
	// one (or specifies a non-positive value). A positive rate limit always
	// gates the middleware; 0 means unlimited.
	rateLimit := req.RateLimit
	if rateLimit <= 0 {
		rateLimit = a.Cfg.APITokenDefaultRateLimit
	}

	id, err := a.DB.CreateAPIToken(tokenStr, req.Name, req.ProjectID, rateLimit, req.QuotaPerDay)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("create_api_token", fmt.Sprintf("创建 API Token: %s (限速 %d/时, 配额 %d/日)", req.Name, rateLimit, req.QuotaPerDay), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusCreated, gin.H{
		"id":           id,
		"token":        tokenStr,
		"name":         req.Name,
		"project_id":   req.ProjectID,
		"rate_limit":   rateLimit,
		"quota_per_day": req.QuotaPerDay,
		"is_active":    true,
	})
}

// AdminUpdateAPIToken updates an API token's name, project, or active status.
func (a *App) AdminUpdateAPIToken(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	var req struct {
		Name        string `json:"name"`
		ProjectID   string `json:"project_id"`
		IsActive    *bool  `json:"is_active"`
		RateLimit   *int   `json:"rate_limit"`
		QuotaPerDay *int   `json:"quota_per_day"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	if err := a.DB.UpdateAPIToken(id, req.Name, req.ProjectID, req.IsActive, req.RateLimit, req.QuotaPerDay); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_api_token", fmt.Sprintf("更新 API Token #%d", id), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

// AdminDeleteAPIToken deletes an API token.
func (a *App) AdminDeleteAPIToken(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	if err := a.DB.DeleteAPIToken(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_api_token", fmt.Sprintf("删除 API Token #%d", id), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// PublicVoteFeedback records an upvote on a feedback from any visitor (M4).
func (a *App) PublicVoteFeedback(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	var voterKey string
	if t := strings.TrimSpace(c.Query("token")); t != "" {
		voterKey = "tok:" + t
	} else {
		ua := c.GetHeader("User-Agent")
		h := sha256.Sum256([]byte(middleware.GetClientIP(c) + "|" + ua))
		voterKey = "anon:" + hex.EncodeToString(h[:])
	}
	already, err := a.DB.InsertVote(id, voterKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "投票失败"})
		return
	}
	votes, _ := a.DB.CountVotes(id)
	c.JSON(http.StatusOK, gin.H{"voted": !already, "votes": votes})
}

// PublicRoadmap returns public roadmap items for a project (M3).
func (a *App) PublicRoadmap(c *gin.Context) {
	slug := strings.TrimSpace(c.Query("slug"))
	category := strings.TrimSpace(c.Query("category"))
	items, err := a.DB.GetPublicRoadmap(slug, category)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if items == nil {
		items = []database.RoadmapItem{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// AdminSetRoadmap toggles public visibility and/or board status of a feedback (M3).
func (a *App) AdminSetRoadmap(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	if _, deny := a.checkFeedbackWritePerm(c, id); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}
	var req struct {
		Public bool   `json:"public"`
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.Status != "" {
		valid := map[string]bool{"planning": true, "in_progress": true, "released": true}
		if !valid[req.Status] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的看板状态"})
			return
		}
	}
	if err := a.DB.SetRoadmap(id, req.Public, req.Status); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("set_roadmap", fmt.Sprintf("反馈 #%d 看板: public=%v status=%s", id, req.Public, req.Status), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

// ========== API Token Auth Middleware ==========

// APITokenAuthMiddleware authenticates requests using Bearer token from API tokens.
// If a valid API token is found, it sets "api_token_project" in the context.
func (a *App) APITokenAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			c.Next()
			return
		}
		tokenStr := strings.TrimPrefix(auth, "Bearer ")
		token, err := a.DB.GetAPITokenByToken(tokenStr)
		if err != nil || token == nil {
			c.Next()
			return
		}
		// Per-hour rate limit (in-memory, single-instance)
		if token.RateLimit > 0 {
			a.tokenMu.Lock()
			hour := time.Now().Format("2006-01-02T15")
			key := tokenStr + "#" + hour
			if a.tokenHourHits[key] >= token.RateLimit {
				a.tokenMu.Unlock()
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "API Token 每小时请求次数超限", "retry_after": 3600})
				return
			}
			a.tokenHourHits[key]++
			a.tokenMu.Unlock()
		}
		// Daily quota
		if token.QuotaPerDay > 0 {
			ok, qerr := a.DB.RecordTokenUsage(tokenStr, token.QuotaPerDay)
			if qerr != nil {
				log.Printf("[API] quota check failed: %v", qerr)
			} else if !ok {
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "API Token 每日配额已用尽"})
				return
			}
		}
		// Valid API token — set project context and skip further auth
		c.Set("api_token_project", token.ProjectID)
		c.Set("api_token_name", token.Name)
		go a.DB.TouchAPIToken(tokenStr)
		c.Next()
	}
}

// SubmitFeedbackWithToken handles feedback submission via API token.
func (a *App) SubmitFeedbackWithToken(c *gin.Context) {
	// Limit request body to configured max upload size
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, a.Cfg.MaxUploadSize)

	projectID, _ := c.Get("api_token_project")
	if projectID == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "无效的 API Token"})
		return
	}

	pid := fmt.Sprintf("%v", projectID)

	// Validate project exists and is active
	proj, err := a.DB.GetProjectBySlug(pid)
	if err != nil || proj == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "项目不存在"})
		return
	}
	if !proj.IsActive {
		c.JSON(http.StatusBadRequest, gin.H{"error": "项目已停用，无法提交反馈"})
		return
	}

	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		CustomData  string `json:"custom_data"`
		Tags        string `json:"tags"`
		ContactName string `json:"contact_name"`
		ContactEmail string `json:"contact_email"`
		Priority    string `json:"priority"`
		Category    string `json:"category"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		if err.Error() == "http: request body too large" {
			maxMB := a.Cfg.MaxUploadSize / 1024 / 1024
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("请求体过大，上限 %dMB", maxMB)})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "标题不能为空"})
		return
	}
	// Validate category against project dictionary
	if req.Category != "" {
		cat, catErr := a.DB.GetCategoryByKey(pid, req.Category)
		if catErr != nil || cat == nil || !cat.IsActive {
			c.JSON(http.StatusBadRequest, gin.H{"error": "分类无效或不存在于该项目字典中"})
			return
		}
	}
	fb := &database.Feedback{
		ProjectID:    pid,
		Title:        req.Title,
		Description:  req.Description,
		CustomData:   req.CustomData,
		Tags:         req.Tags,
		ContactName:  req.ContactName,
		ContactEmail: req.ContactEmail,
		Priority:     req.Priority,
		Category:     req.Category,
		ClientIP:     middleware.GetClientIP(c),
		Status:       "pending",
	}

	// Generate tracking token
	trackingBytes := make([]byte, 16)
	rand.Read(trackingBytes)
	fb.TrackingToken = hex.EncodeToString(trackingBytes)

	id, err := a.DB.ImportFeedback(fb, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "提交失败"})
		return
	}
	fb.ID = id

	tokenName, _ := c.Get("api_token_name")
	a.DB.InsertAuditLog("api_submit", fmt.Sprintf("API Token 提交反馈 #%d: %s", id, req.Title), fmt.Sprintf("%v", tokenName), fb.ClientIP)

	go a.sendWebhookEvent("new_feedback", map[string]interface{}{
		"id":         fb.ID,
		"project_id": fb.ProjectID,
		"title":      fb.Title,
		"source":     "api_token",
	}, fb)

	c.JSON(http.StatusCreated, gin.H{
		"id":             fb.ID,
		"tracking_token": fb.TrackingToken,
		"message":        "提交成功",
	})
}

// ========== Bulk Operations (Extended) ==========

// AdminBulkUpdateTags updates tags on multiple feedbacks.
func (a *App) AdminBulkUpdateTags(c *gin.Context) {
	var req struct {
		IDs  []int64 `json:"ids"`
		Tags string  `json:"tags"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未选择反馈"})
		return
	}

	affected, err := a.DB.BulkUpdateFeedbackTags(req.IDs, req.Tags)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("bulk_tags", fmt.Sprintf("批量更新 %d 条反馈标签", affected), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已更新 %d 条", affected), "affected": affected})
}

// AdminBulkUpdateAssignee updates assignee on multiple feedbacks.
func (a *App) AdminBulkUpdateAssignee(c *gin.Context) {
	var req struct {
		IDs      []int64 `json:"ids"`
		Assignee string  `json:"assignee"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未选择反馈"})
		return
	}

	affected, err := a.DB.BulkUpdateFeedbackAssignee(req.IDs, req.Assignee)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("bulk_assignee", fmt.Sprintf("批量更新 %d 条反馈指派人", affected), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已更新 %d 条", affected), "affected": affected})
}

// AdminBulkUpdatePriority updates priority on multiple feedbacks.
func (a *App) AdminBulkUpdatePriority(c *gin.Context) {
	var req struct {
		IDs      []int64 `json:"ids"`
		Priority string  `json:"priority"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未选择反馈"})
		return
	}

	validPriorities := map[string]bool{"": true, "low": true, "medium": true, "high": true, "urgent": true}
	if !validPriorities[req.Priority] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的优先级"})
		return
	}

	affected, err := a.DB.BulkUpdateFeedbackPriority(req.IDs, req.Priority)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("bulk_priority", fmt.Sprintf("批量更新 %d 条反馈优先级", affected), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已更新 %d 条", affected), "affected": affected})
}

// ========== CSV Import ==========

// AdminImportCSV imports feedbacks from a CSV file.
// Supports both English and Chinese column headers:
//
//	title/标题, description/描述, status/状态, tags/标签,
//	assignee/指派, contact_name/联系人, contact_email/联系邮箱,
//	priority/优先级, created_at/提交时间, project_id/项目, project/项目
func (a *App) AdminImportCSV(c *gin.Context) {
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请上传 CSV 文件"})
		return
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CSV 解析失败: " + err.Error()})
		return
	}

	if len(records) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CSV 文件为空或只有表头"})
		return
	}

	// Chinese → English header alias map
	cnToEn := map[string]string{
		"标题":     "title",
		"描述":     "description",
		"状态":     "status",
		"标签":     "tags",
		"指派":     "assignee",
		"联系人":    "contact_name",
		"联系邮箱":   "contact_email",
		"优先级":    "priority",
		"提交时间":   "created_at",
		"项目":     "project_id",
		"自定义字段":  "custom_data",
		"附件":     "file_paths",
		"来源ip":   "client_ip",
	}

	// Parse header to find column indices (normalized to English names)
	header := records[0]
	colIndex := map[string]int{}
	for i, h := range header {
		normalized := strings.TrimSpace(strings.ToLower(h))
		if en, ok := cnToEn[normalized]; ok {
			normalized = en
		}
		colIndex[normalized] = i
	}

	if _, ok := colIndex["title"]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CSV 缺少必要列: title (标题)"})
		return
	}

	// Validate project_id from form or CSV column
	globalProjectID := c.PostForm("project_id")

	// If no form project_id, check if CSV has a project_id column and validate first row
	if globalProjectID == "" {
		if _, hasProjCol := colIndex["project_id"]; !hasProjCol {
			// No project specified anywhere — validate "default" exists
			proj, projErr := a.DB.GetProjectBySlug("default")
			if projErr != nil || proj == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "未指定项目且 default 项目不存在，请通过表单 project_id 或 CSV 项目列指定"})
				return
			}
			if !proj.IsActive {
				c.JSON(http.StatusBadRequest, gin.H{"error": "default 项目已停用"})
				return
			}
			globalProjectID = "default"
		}
		// else: per-row project_id will be used
	} else {
		// Validate form-specified project exists
		proj, projErr := a.DB.GetProjectBySlug(globalProjectID)
		if projErr != nil || proj == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "项目不存在: " + globalProjectID})
			return
		}
		if !proj.IsActive {
			c.JSON(http.StatusBadRequest, gin.H{"error": "项目已停用: " + globalProjectID})
			return
		}
	}

	// parseCreatedAt tries multiple formats: unix timestamp, "2006-01-02 15:04:05", "2006-01-02"
	parseCreatedAt := func(s string) int64 {
		s = strings.TrimSpace(s)
		if s == "" {
			return 0
		}
		// Try unix timestamp
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			return v
		}
		// Try common datetime formats
		for _, layout := range []string{
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05",
			"2006-01-02T15:04:05Z",
			"2006-01-02",
			"2006/01/02 15:04:05",
			"2006/01/02",
		} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.Unix()
			}
		}
		return 0
	}

	imported := 0
	errors := []string{}
	for i, row := range records[1:] {
		lineNum := i + 2
		getCol := func(name string) string {
			if idx, ok := colIndex[name]; ok && idx < len(row) {
				return strings.TrimSpace(row[idx])
			}
			return ""
		}

		title := getCol("title")
		if title == "" {
			errors = append(errors, fmt.Sprintf("第 %d 行: 标题为空，已跳过", lineNum))
			continue
		}

		// Determine project_id: per-row > form > default
		pid := globalProjectID
		if rowProj := getCol("project_id"); rowProj != "" && globalProjectID == "" {
			// Validate per-row project
			proj, projErr := a.DB.GetProjectBySlug(rowProj)
			if projErr != nil || proj == nil {
				errors = append(errors, fmt.Sprintf("第 %d 行: 项目不存在: %s", lineNum, rowProj))
				continue
			}
			if !proj.IsActive {
				errors = append(errors, fmt.Sprintf("第 %d 行: 项目已停用: %s", lineNum, rowProj))
				continue
			}
			pid = rowProj
		}

		createdAtUnix := parseCreatedAt(getCol("created_at"))

		fb := &database.Feedback{
			ProjectID:    pid,
			Title:        title,
			Description:  getCol("description"),
			Status:       getCol("status"),
			Tags:         getCol("tags"),
			Assignee:     getCol("assignee"),
			ContactName:  getCol("contact_name"),
			ContactEmail: getCol("contact_email"),
			Priority:     getCol("priority"),
			CustomData:   getCol("custom_data"),
			ClientIP:     "csv-import",
		}

		if _, err := a.DB.ImportFeedback(fb, createdAtUnix); err != nil {
			errors = append(errors, fmt.Sprintf("第 %d 行: 导入失败: %v", lineNum, err))
			continue
		}
		imported++
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("csv_import", fmt.Sprintf("CSV 导入 %d 条反馈", imported), fmt.Sprintf("%v", user), clientIP)

	result := gin.H{
		"imported": imported,
		"total":    len(records) - 1,
	}
	if len(errors) > 0 {
		result["errors"] = errors
	}
	c.JSON(http.StatusOK, result)
}

// ========== Data Archive & Cleanup ==========

// AdminArchiveOldFeedbacks archives old pending/processing feedbacks.
func (a *App) AdminArchiveOldFeedbacks(c *gin.Context) {
	var req struct {
		DaysOld int `json:"days_old"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.DaysOld <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请指定有效天数"})
		return
	}

	affected, err := a.DB.ArchiveOldFeedbacks(req.DaysOld)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "归档失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("archive", fmt.Sprintf("归档 %d 条超过 %d 天的反馈", affected, req.DaysOld), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{
		"message":  fmt.Sprintf("已归档 %d 条反馈", affected),
		"archived": affected,
	})
}

// AdminPruneOldBackups removes old backup files.
func (a *App) AdminPruneOldBackups(c *gin.Context) {
	var req struct {
		DaysOld int `json:"days_old"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.DaysOld <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请指定有效天数"})
		return
	}

	backupDir := filepath.Join(a.Cfg.DataDir, "backups")
	pruned, err := a.DB.PruneOldBackups(backupDir, req.DaysOld)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "清理失败: " + err.Error()})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("prune_backups", fmt.Sprintf("清理 %d 个超过 %d 天的备份", pruned, req.DaysOld), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("已清理 %d 个备份文件", pruned),
		"pruned":  pruned,
	})
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

// ========== Project Archive ==========

// AdminArchiveProject archives or unarchives a project.
func (a *App) AdminArchiveProject(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	var req struct {
		Archived bool `json:"archived"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	if err := a.DB.ArchiveProject(id, req.Archived); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	action := "归档"
	if !req.Archived {
		action = "取消归档"
	}
	a.DB.InsertAuditLog("archive_project", fmt.Sprintf("%s项目 #%d", action, id), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "项目已" + action})
}

// ========== Category Management ==========

func (a *App) AdminListCategories(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	proj, projErr := a.DB.GetProject(id)
	if projErr != nil || proj == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return
	}
	categories, err := a.DB.ListCategories(proj.Slug)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if categories == nil {
		categories = []database.Category{}
	}
	c.JSON(http.StatusOK, gin.H{"categories": categories})
}

func (a *App) AdminCreateCategory(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	proj, projErr := a.DB.GetProject(id)
	if projErr != nil || proj == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return
	}
	slug := proj.Slug
	var req struct {
		Key       string `json:"key"`
		Name      string `json:"name"`
		Color     string `json:"color"`
		SortOrder int    `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.Key == "" || req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key 和 name 不能为空"})
		return
	}
	// Check duplicate key
	existing, _ := a.DB.GetCategoryByKey(slug, req.Key)
	if existing != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "分类 key 已存在"})
		return
	}
	catID, err := a.DB.CreateCategory(slug, req.Key, req.Name, req.Color, req.SortOrder)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("create_category", fmt.Sprintf("创建分类 %s: %s", req.Key, req.Name), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusCreated, gin.H{"id": catID, "key": req.Key, "name": req.Name})
}

func (a *App) AdminUpdateCategory(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	var req struct {
		Name      string `json:"name"`
		Color     string `json:"color"`
		SortOrder int    `json:"sort_order"`
		IsActive  *bool  `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	if err := a.DB.UpdateCategory(id, req.Name, req.Color, req.SortOrder, isActive); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_category", fmt.Sprintf("更新分类 #%d", id), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

func (a *App) AdminDeleteCategory(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	if err := a.DB.DeleteCategory(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_category", fmt.Sprintf("删除分类 #%d", id), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

func (a *App) AdminUpdateFeedbackCategory(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	var req struct {
		Category string `json:"category"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	// Verify feedback exists and check write permission
	fb, deny := a.checkFeedbackWritePerm(c, id)
	if deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}
	// If category is not empty, verify it belongs to the project's dictionary
	if req.Category != "" {
		cat, catErr := a.DB.GetCategoryByKey(fb.ProjectID, req.Category)
		if catErr != nil || cat == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "分类不存在于该项目字典中"})
			return
		}
		if !cat.IsActive {
			c.JSON(http.StatusBadRequest, gin.H{"error": "分类已停用"})
			return
		}
		// Also check write permission on the TARGET category (not just current)
		roleStr, _ := c.Get("admin_role")
		if roleStr != "admin" {
			username, _ := c.Get("admin_user")
			if usernameStr, ok := username.(string); ok {
				admin, _ := a.DB.GetAdminByUsername(usernameStr)
				if admin != nil {
					targetRole := a.DB.GetEffectiveRole(admin.ID, fb.ProjectID, req.Category)
					roleLevel := map[string]int{"viewer": 1, "editor": 2, "manager": 3, "admin": 4}
					if roleLevel[targetRole] < 2 {
						c.JSON(http.StatusForbidden, gin.H{"error": "您对目标分类无编辑权限"})
						return
					}
				}
			}
		}
	}
	if err := a.DB.UpdateFeedbackCategory(id, req.Category); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_category", fmt.Sprintf("反馈 #%d 分类更新为 %s", id, req.Category), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusOK, gin.H{"message": "分类已更新"})
}

func (a *App) AdminBulkUpdateCategory(c *gin.Context) {
	var req struct {
		IDs      []int64 `json:"ids"`
		Category string  `json:"category"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未选择反馈"})
		return
	}
	affected, err := a.DB.BulkUpdateCategory(req.IDs, req.Category)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("bulk_update_category", fmt.Sprintf("批量更新 %d 条反馈分类", affected), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusOK, gin.H{"affected": affected})
}
