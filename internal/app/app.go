package app

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"feedshit/internal/config"
	"feedshit/internal/database"
	"feedshit/internal/email"
	"feedshit/internal/middleware"
)

// App holds all shared dependencies for HTTP handlers.
type App struct {
	Cfg     *config.Config
	DB      *database.Database
	SM      *middleware.SessionManager
	RL      *middleware.RateLimiter
	Mailer  *email.Mailer
}

// New creates a new App instance.
func New(cfg *config.Config, db *database.Database, sm *middleware.SessionManager, rl *middleware.RateLimiter, mailer *email.Mailer) *App {
	return &App{Cfg: cfg, DB: db, SM: sm, RL: rl, Mailer: mailer}
}

// ========== Public Submission ==========

func (a *App) SubmitFeedback(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, a.Cfg.MaxUploadSize)

	if err := c.Request.ParseMultipartForm(a.Cfg.MaxUploadSize); err != nil {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "请求体过大，上限 20MB"})
		return
	}

	projectID := strings.TrimSpace(c.PostForm("project_id"))
	if projectID == "" {
		projectID = "default"
	}

	// Check if project is active (if it exists in the projects table)
	if !a.DB.IsProjectActive(projectID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "该项目已停用，无法提交反馈"})
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

	timestamp := c.GetHeader("X-PoW-Timestamp")
	nonce := c.GetHeader("X-PoW-Nonce")
	if !middleware.VerifyPoW(projectID, timestamp, nonce, a.Cfg.PoWDifficulty) {
		c.JSON(http.StatusForbidden, gin.H{"error": "工作量证明校验失败"})
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
			c.JSON(http.StatusInternalServerError, gin.H{"error": "文件保存失败: " + err.Error()})
			return
		}
		savedPaths = append(savedPaths, p)
	}
	for _, fh := range form.File["files"] {
		p, err := a.saveUpload(fh, projectID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "文件保存失败: " + err.Error()})
			return
		}
		savedPaths = append(savedPaths, p)
	}

	filePathsJSON, _ := json.Marshal(savedPaths)
	clientIP := middleware.GetClientIP(c)

	fb := &database.Feedback{
		ProjectID:   projectID,
		Title:       title,
		Description: description,
		CustomData:  customData,
		FilePaths:   string(filePathsJSON),
		ClientIP:    clientIP,
	}

	id, err := a.DB.InsertFeedback(fb)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库写入失败"})
		return
	}
	fb.ID = id

	go a.Mailer.SendFeedbackNotification(fb)

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

func (a *App) saveUpload(fh *multipart.FileHeader, projectID string) (string, error) {
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !allowedExtensions[ext] {
		return "", fmt.Errorf("不允许的文件类型: %s", ext)
	}

	origName := filepath.Base(fh.Filename)
	ts := time.Now().UTC().Format("20060102_150405")
	uid := uuid.New().String()[:8]
	safeName := fmt.Sprintf("%s_%s_%s", ts, uid, origName)

	relDir := filepath.Join("uploads", projectID)
	absDir := filepath.Join(a.Cfg.DataDir, relDir)
	if err := os.MkdirAll(absDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	relPath := filepath.Join(relDir, safeName)
	absPath := filepath.Join(a.Cfg.DataDir, relPath)

	src, err := fh.Open()
	if err != nil {
		return "", fmt.Errorf("open uploaded file: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(absPath)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return "", fmt.Errorf("write file: %w", err)
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

	if !middleware.SecureCompare(req.Username, effectiveUser) || !middleware.SecureCompare(req.Password, effectivePwd) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	token := a.SM.Create(req.Username)
	c.SetCookie("admin_session", token, 86400, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"message": "登录成功"})
}

func (a *App) AdminLogout(c *gin.Context) {
	token, _ := c.Get("session_token")
	if t, ok := token.(string); ok {
		a.SM.Revoke(t)
	}
	c.SetCookie("admin_session", "", -1, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"message": "已退出"})
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
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	list, total, err := a.DB.ListFeedbacks(project, limit, offset)
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

func (a *App) AdminGetConfig(c *gin.Context) {
	configs, err := a.DB.GetAllConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取配置失败"})
		return
	}
	for i := range configs {
		if (configs[i].Key == "smtp_pass" || configs[i].Key == "admin_password" || configs[i].Key == "ext_pass") && configs[i].Value != "" {
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
		if (item.Key == "smtp_pass" || item.Key == "admin_password" || item.Key == "ext_pass") && strings.Contains(item.Value, "*") {
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
	for _, p := range paths {
		absPath := filepath.Join(a.Cfg.DataDir, filepath.FromSlash(p))
		os.Remove(absPath)
	}

	if err := a.DB.DeleteFeedback(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
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
		DBType        string `json:"db_type"`
		ExtDBType     string `json:"ext_db_type"`
		ExtHost       string `json:"ext_host"`
		ExtPort       string `json:"ext_port"`
		ExtUser       string `json:"ext_user"`
		ExtPass       string `json:"ext_pass"`
		ExtDBName     string `json:"ext_db_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	if len(req.AdminUsername) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名至少 2 个字符"})
		return
	}
	if len(req.AdminPassword) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码长度不能少于 6 位"})
		return
	}

	// Save admin credentials to DB
	a.Cfg.AdminUsername = req.AdminUsername
	a.Cfg.AdminPassword = req.AdminPassword
	a.DB.SetConfig("admin_username", req.AdminUsername, "管理员用户名")
	a.DB.SetConfig("admin_password", req.AdminPassword, "管理员密码")

	// Save DB config
	dbType := req.DBType
	if dbType == "" {
		dbType = "sqlite"
	}
	a.DB.SetConfig("db_type", dbType, "数据库类型")

	if dbType == "external" {
		a.DB.SetConfig("ext_db_type", req.ExtDBType, "外部数据库类型")
		a.DB.SetConfig("ext_host", req.ExtHost, "外部数据库主机")
		a.DB.SetConfig("ext_port", req.ExtPort, "外部数据库端口")
		a.DB.SetConfig("ext_user", req.ExtUser, "外部数据库用户名")
		a.DB.SetConfig("ext_pass", req.ExtPass, "外部数据库密码")
		a.DB.SetConfig("ext_db_name", req.ExtDBName, "外部数据库名")
	}

	// Mark setup complete
	a.DB.SetConfig("setup_complete", "true", "初始安装已完成")

	c.JSON(http.StatusOK, gin.H{"message": "设置完成"})
}

// Dashboard handles GET /api/v1/dashboard
// Returns stats and recent feedback for the landing page dashboard.
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
	if err := a.DB.DeleteProject(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "项目已删除"})
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
	w.Write([]string{"ID", "项目", "标题", "描述", "自定义字段", "来源IP", "提交时间"})
	for _, fb := range feedbacks {
		w.Write([]string{
			strconv.FormatInt(fb.ID, 10),
			fb.ProjectID,
			fb.Title,
			fb.Description,
			fb.CustomData,
			fb.ClientIP,
			fb.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	w.Flush()
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
	if !middleware.SecureCompare(req.OldPassword, effectivePwd) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "当前密码错误"})
		return
	}

	if req.Username != "" && len(req.Username) >= 2 {
		a.Cfg.AdminUsername = req.Username
		a.DB.SetConfig("admin_username", req.Username, "管理员用户名")
	}
	if req.NewPassword != "" && len(req.NewPassword) >= 6 {
		a.Cfg.AdminPassword = req.NewPassword
		a.DB.SetConfig("admin_password", req.NewPassword, "管理员密码")
	}

	c.JSON(http.StatusOK, gin.H{"message": "账户信息已更新"})
}

// AdminGetSystemConfig returns system-level config.
func (a *App) AdminGetSystemConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"base_url":          a.Cfg.BaseURL,
		"pow_difficulty":    a.Cfg.PoWDifficulty,
		"rate_limit_per_hr": a.Cfg.RateLimitPerHour,
		"max_upload_mb":     a.Cfg.MaxUploadSize / 1024 / 1024,
	})
}

// AdminUpdateSystemConfig updates system-level config.
func (a *App) AdminUpdateSystemConfig(c *gin.Context) {
	var req struct {
		BaseURL       string `json:"base_url"`
		PoWDifficulty int    `json:"pow_difficulty"`
		RateLimit     int    `json:"rate_limit_per_hr"`
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
	c.JSON(http.StatusOK, gin.H{"message": "系统设置已保存"})
}
