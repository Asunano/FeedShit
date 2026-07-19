package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"feedshit/internal/database"
	"feedshit/internal/middleware"
)

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
		"id":            id,
		"token":         tokenStr,
		"name":          req.Name,
		"project_id":    req.ProjectID,
		"rate_limit":    rateLimit,
		"quota_per_day": req.QuotaPerDay,
		"is_active":     true,
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
		Title        string `json:"title"`
		Description  string `json:"description"`
		CustomData   string `json:"custom_data"`
		Tags         string `json:"tags"`
		ContactName  string `json:"contact_name"`
		ContactEmail string `json:"contact_email"`
		Priority     string `json:"priority"`
		Category     string `json:"category"`
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
