package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"feedshit/internal/database"
	"feedshit/internal/email"
	"feedshit/internal/middleware"
)

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

	// F18: Validate custom_data against the project's form_schema
	proj, projErr := a.DB.GetProjectBySlug(projectID)
	if projErr == nil && proj != nil {
		if err := validateFormSchema(proj.FormSchema, customData); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
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
	if _, err := rand.Read(tokenBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成跟踪令牌失败"})
		return
	}
	trackingToken := hex.EncodeToString(tokenBytes)

	fb := &database.Feedback{
		ProjectID:     projectID,
		Title:         title,
		Description:   description,
		CustomData:    customData,
		FilePaths:     string(filePathsJSON),
		ClientIP:      clientIP,
		Status:        database.StatusPending,
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

// ========== Feedback CRUD ==========

func (a *App) AdminListFeedbacks(c *gin.Context) {
	project := c.Query("project")
	keyword := c.Query("keyword")
	status := c.Query("status")
	priority := c.Query("priority")
	assignee := c.Query("assignee")
	category := c.Query("category")
	trackingToken := c.Query("tracking_token")
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
			admin, err := a.DB.GetAdminByUsername(usernameStr)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "查询用户失败"})
				return
			}
			if admin == nil {
				// Non-admin user without a valid admin record → no access
				c.JSON(http.StatusOK, gin.H{"feedbacks": []database.Feedback{}, "total": 0, "assignees": []string{}})
				return
			}
			plan, err := a.DB.GetAdminAccessPlan(admin.ID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "查询权限失败"})
				return
			}
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
	} else {
		// Admin role: use query param if specified
		if project != "" {
			projectIDs = []string{project}
		}
	}

	var list []database.Feedback
	var total int
	var err error

	if keyword != "" || status != "" || priority != "" || assignee != "" || category != "" || trackingToken != "" {
		list, total, err = a.DB.SearchFeedbacks(projectIDs, accessPlan, keyword, status, priority, assignee, category, trackingToken, limit, offset)
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

	// Determine effective status: if request provides empty status, preserve existing value
	// This allows callers to update tags without touching status.
	effectiveStatus := req.Status
	if effectiveStatus == "" {
		effectiveStatus = oldFb.Status
	} else if !database.ValidStatuses[effectiveStatus] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的状态值"})
		return
	}
	statusChanged := effectiveStatus != oldFb.Status

	if err := a.DB.UpdateFeedbackStatus(id, effectiveStatus, req.Tags); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_status", fmt.Sprintf("反馈 #%d 状态更新为 %s", id, effectiveStatus), fmt.Sprintf("%v", user), clientIP)

	// Notify submitter only when status actually changed
	if statusChanged && oldFb.ContactEmail != "" {
		statusLabels := database.StatusLabels
		label := statusLabels[effectiveStatus]
		if label == "" {
			label = effectiveStatus
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
		if effectiveStatus == database.StatusResolved && oldFb.ContactEmail != "" {
			trackURL := a.Cfg.BaseURL + "/track#token=" + oldFb.TrackingToken
			go a.Mailer.SendCSATInvite(oldFb, trackURL)
		}
	}

	// Webhook notification
	go a.sendWebhookEvent("status_change", map[string]interface{}{
		"id":         oldFb.ID,
		"project_id": oldFb.ProjectID,
		"title":      oldFb.Title,
		"status":     effectiveStatus,
		"operator":   fmt.Sprintf("%v", user),
	}, oldFb)

	c.JSON(http.StatusOK, gin.H{"message": "状态已更新"})
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

	// Webhook: feedback deleted
	go a.sendWebhookEvent("feedback_deleted", map[string]interface{}{
		"id":         id,
		"project_id": fb.ProjectID,
		"title":      fb.Title,
	}, fb)

	c.JSON(http.StatusOK, gin.H{"message": msg})
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

	// F4: Send email notification to the assignee if it's a known admin
	if req.Assignee != "" {
		assigneeEmail := a.DB.GetAdminEmail(req.Assignee)
		if assigneeEmail != "" {
			go a.Mailer.Send(assigneeEmail,
				fmt.Sprintf("[FeedShit] 您被指派处理反馈 #%d", id),
				fmt.Sprintf(`<html><body style="font-family:-apple-system,sans-serif;color:#333;max-width:600px;margin:0 auto">
<h2>📋 反馈指派通知</h2>
<p>反馈 <strong>#%d</strong> 已被指派给您处理：</p>
<table style="width:100%%;border-collapse:collapse;margin:16px 0">
<tr><td style="padding:8px;border-bottom:1px solid #eee;font-weight:bold;width:100px">标题</td><td style="padding:8px;border-bottom:1px solid #eee">%s</td></tr>
<tr><td style="padding:8px;border-bottom:1px solid #eee;font-weight:bold">项目</td><td style="padding:8px;border-bottom:1px solid #eee">%s</td></tr>
</table>
<p><a href="%s/admin/#feedback/%d" style="display:inline-block;padding:10px 20px;background:#e53e3e;color:white;text-decoration:none;border-radius:4px">在后台查看</a></p>
<p style="color:#999;font-size:12px;margin-top:24px">此邮件由 FeedShit 自动发送</p>
</body></html>`,
					fb.ID, fb.Title, fb.ProjectID, a.Cfg.BaseURL, fb.ID))
		}
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

	// RBAC: verify write permission on all IDs
	if deny := a.checkBulkWritePerm(c, req.IDs); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
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

	// Webhook: bulk operation
	a.sendBulkWebhook("bulk_delete", req.IDs, affected)

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

	// RBAC: verify write permission on all IDs
	if deny := a.checkBulkWritePerm(c, req.IDs); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}

	validStatuses := database.ValidStatuses
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

	// Webhook: bulk operation
	a.sendBulkWebhook("bulk_update_status", req.IDs, affected)

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已更新 %d 条反馈状态", affected), "affected": affected})
}

// sendBulkWebhook fires a bulk_operation webhook event for batch operations.
func (a *App) sendBulkWebhook(operation string, ids []int64, affected int64) {
	go a.sendWebhookEvent("bulk_operation", map[string]interface{}{
		"operation": operation,
		"ids":       ids,
		"affected":  affected,
	}, nil)
}

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

	// RBAC: verify write permission on all IDs
	if deny := a.checkBulkWritePerm(c, req.IDs); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
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

	// RBAC: verify write permission on all IDs
	if deny := a.checkBulkWritePerm(c, req.IDs); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
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

	// RBAC: verify write permission on all IDs
	if deny := a.checkBulkWritePerm(c, req.IDs); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}

	validPriorities := database.ValidPriorities
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

	validPriorities := database.ValidPriorities
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

	// F8: Merge notes and votes from the duplicate into the target
	if err := a.DB.MergeFeedback(id, req.DuplicateOf); err != nil {
		log.Printf("[MERGE] WARN: partial merge for #%d → #%d: %v", id, req.DuplicateOf, err)
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("mark_duplicate", fmt.Sprintf("反馈 #%d 标记为 #%d 的重复", id, req.DuplicateOf), fmt.Sprintf("%v", user), clientIP)

	// F5: Webhook event for duplicate marking
	go a.sendWebhookEvent("mark_duplicate", map[string]interface{}{
		"id":           id,
		"duplicate_of": req.DuplicateOf,
		"project_id":   fb.ProjectID,
		"title":        fb.Title,
	}, fb)

	c.JSON(http.StatusOK, gin.H{"message": "已标记为重复"})
}

// AdminUnmarkDuplicate clears the duplicate flag on a feedback.
func (a *App) AdminUnmarkDuplicate(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
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

	if err := a.DB.UnmarkDuplicate(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("unmark_duplicate", fmt.Sprintf("反馈 #%d 取消重复标记", id), fmt.Sprintf("%v", user), clientIP)

	// F5: Webhook event for unmark duplicate
	go a.sendWebhookEvent("unmark_duplicate", map[string]interface{}{
		"id":         id,
		"project_id": fb.ProjectID,
		"title":      fb.Title,
	}, fb)

	c.JSON(http.StatusOK, gin.H{"message": "已取消重复标记"})
}

// ========== Category on Feedback ==========

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
					roleLevel := middleware.RoleLevel
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

	// F5: Webhook event for category change
	go a.sendWebhookEvent("category_change", map[string]interface{}{
		"id":         id,
		"project_id": fb.ProjectID,
		"title":      fb.Title,
		"category":   req.Category,
	}, fb)

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

	// RBAC: verify write permission on all IDs
	if deny := a.checkBulkWritePerm(c, req.IDs); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
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

// AdminGetTags returns tag suggestions for autocomplete (F20).
// Route: GET /api/v1/admin/tags?q=prefix
func (a *App) AdminGetTags(c *gin.Context) {
	prefix := strings.TrimSpace(c.Query("q"))
	tags, err := a.DB.GetTags(prefix)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tags": tags})
}
