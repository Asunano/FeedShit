package app

import (
	"bytes"
	"encoding/csv"
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
}

// New creates a new App instance.
func New(cfg *config.Config, db *database.Database, sm *middleware.SessionManager, rl *middleware.RateLimiter, mailer *email.Mailer) *App {
	return &App{
		Cfg:          cfg,
		DB:           db,
		SM:           sm,
		RL:           rl,
		Mailer:       mailer,
		NonceCache:   middleware.NewNonceCache(),
		LoginTracker: middleware.NewLoginAttemptTracker(10),
	}
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

	fb := &database.Feedback{
		ProjectID:    projectID,
		Title:        title,
		Description:  description,
		CustomData:   customData,
		FilePaths:    string(filePathsJSON),
		ClientIP:     clientIP,
		Status:       "pending",
		ContactName:  contactName,
		ContactEmail: contactEmail,
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
		"message": "反馈提交成功",
		"id":      fb.ID,
	})
}

// allowedExtensions defines the file types accepted for upload.
var allowedExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".svg": true,
	".log": true, ".txt": true, ".csv": true, ".json": true,
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

	if _, err := io.Copy(dst, src); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
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

	// Check credentials: DB-stored (from setup) takes priority, fallback to env var
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

	if !middleware.SecureCompare(req.Username, effectiveUser) || !checkPassword(req.Password, effectivePwd) {
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

	token := a.SM.Create(req.Username)
	csrfToken := a.SM.GetCSRFToken(token)
	c.SetCookie("admin_session", token, 86400, "/", "", false, true)
	middleware.SetCSRFCookie(c, csrfToken)

	a.DB.InsertAuditLog("login", "管理员登录", req.Username, clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "登录成功"})
}

func (a *App) AdminLogout(c *gin.Context) {
	token, _ := c.Get("session_token")
	if t, ok := token.(string); ok {
		a.SM.Revoke(t)
	}
	c.SetCookie("admin_session", "", -1, "/", "", false, true)
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
	c.JSON(http.StatusOK, gin.H{
		"total_feedbacks": total,
		"total_projects":  projects,
		"today_feedbacks": today,
	})
}

func (a *App) AdminListFeedbacks(c *gin.Context) {
	project := c.Query("project")
	keyword := c.Query("keyword")
	status := c.Query("status")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var list []database.Feedback
	var total int
	var err error

	if keyword != "" || status != "" {
		list, total, err = a.DB.SearchFeedbacks(project, keyword, status, limit, offset)
	} else {
		list, total, err = a.DB.ListFeedbacks(project, limit, offset)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}

	projList, _ := a.DB.GetProjects()

	c.JSON(http.StatusOK, gin.H{
		"feedbacks": list,
		"total":     total,
		"projects":  projList,
	})
}

func (a *App) AdminGetFeedback(c *gin.Context) {
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

	if err := a.DB.UpdateFeedbackStatus(id, req.Status, req.Tags); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_status", fmt.Sprintf("反馈 #%d 状态更新为 %s", id, req.Status), fmt.Sprintf("%v", user), clientIP)

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
	reqPath := c.Param("filepath")
	if reqPath == "" {
		p := c.Param("path")
		if len(p) >= 7 && p[:7] == "/files/" {
			reqPath = p[7:]
		}
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

	absPath := filepath.Join(a.Cfg.DataDir, cleaned)
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
	if !strings.HasPrefix(absResolved, absDataDirResolved) {
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

	// Mark setup complete
	a.DB.SetConfig("setup_complete", "true", "初始安装已完成")

	c.JSON(http.StatusOK, gin.H{"message": "设置完成"})
}

// Dashboard handles GET /api/v1/dashboard
func (a *App) Dashboard(c *gin.Context) {
	total, projects, today, err := a.DB.GetStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取统计失败"})
		return
	}

	recent, _, _ := a.DB.ListFeedbacks("", 10, 0)

	c.JSON(http.StatusOK, gin.H{
		"total_feedbacks": total,
		"total_projects":  projects,
		"today_feedbacks": today,
		"recent":          recent,
	})
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
	// Only return active projects
	active := make([]database.Project, 0)
	for _, p := range projects {
		if p.IsActive {
			active = append(active, p)
		}
	}
	c.JSON(http.StatusOK, gin.H{"projects": active})
}

// ========== Admin: Project Management ==========

func (a *App) AdminListProjects(c *gin.Context) {
	projects, err := a.DB.ListProjects()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取项目列表失败: " + err.Error()})
		return
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
		FormSchema  string `json:"form_schema"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	// If form_schema not provided, preserve existing
	formSchema := req.FormSchema
	if formSchema == "" {
		existing, err := a.DB.GetProject(id)
		if err == nil {
			formSchema = existing.FormSchema
		}
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
				os.Remove(absPath) // best-effort cleanup
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
	c.JSON(http.StatusOK, gin.H{
		"base_url":          a.Cfg.BaseURL,
		"pow_difficulty":    a.Cfg.PoWDifficulty,
		"rate_limit_per_hr": a.Cfg.RateLimitPerHour,
		"max_upload_mb":     a.Cfg.MaxUploadSize / 1024 / 1024,
		"webhook_url":       webhookURL,
	})
}

// AdminUpdateSystemConfig updates system-level config.
func (a *App) AdminUpdateSystemConfig(c *gin.Context) {
	var req struct {
		BaseURL       string `json:"base_url"`
		PoWDifficulty int    `json:"pow_difficulty"`
		RateLimit     int    `json:"rate_limit_per_hr"`
		WebhookURL    string `json:"webhook_url"`
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
		a.DB.SetConfig("webhook_url", req.WebhookURL, "Webhook 通知 URL")
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

func (a *App) SendWebhookNotification(fb *database.Feedback) {
	webhookURL := a.DB.GetConfig("webhook_url")
	if webhookURL == "" {
		webhookURL = a.Cfg.WebhookURL
	}
	if webhookURL == "" {
		return
	}

	payload := map[string]interface{}{
		"id":          fb.ID,
		"project_id":  fb.ProjectID,
		"title":       fb.Title,
		"description": fb.Description,
		"custom_data": fb.CustomData,
		"client_ip":   fb.ClientIP,
		"created_at":  fb.CreatedAt.Format(time.RFC3339),
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[WEBHOOK] Failed to marshal payload: %v", err)
		return
	}

	resp, err := http.Post(webhookURL, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		log.Printf("[WEBHOOK] Failed to send notification for feedback #%d: %v", fb.ID, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		log.Printf("[WEBHOOK] Notification for feedback #%d returned status %d", fb.ID, resp.StatusCode)
	} else {
		log.Printf("[WEBHOOK] Notification sent for feedback #%d", fb.ID)
	}
}

// ========== Feedback Notes (Replies / Internal Notes) ==========

func (a *App) AdminAddFeedbackNote(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	// Verify feedback exists
	if _, err := a.DB.GetFeedback(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
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

	c.JSON(http.StatusCreated, gin.H{"message": "备注已添加", "id": noteID})
}

func (a *App) AdminListFeedbackNotes(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
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

	c.JSON(http.StatusOK, gin.H{
		"daily_trend":       trend,
		"status_distribution": statusDist,
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
