package app

import (
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
	"feedshit/internal/middleware"
)

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

	// Validate required fields
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "项目名称不能为空"})
		return
	}

	// Validate slug format when provided (empty = keep existing)
	if req.Slug != "" {
		for _, ch := range req.Slug {
			if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
				c.JSON(http.StatusBadRequest, gin.H{"error": "标识只能包含小写字母、数字、连字符和下划线"})
				return
			}
		}
	} else {
		// Preserve existing slug when not provided
		req.Slug = existing.Slug
	}

	// If slug changed, record old slug in history and check uniqueness
	if req.Slug != existing.Slug {
		// Check slug uniqueness
		if existingBySlug, err := a.DB.GetProjectBySlug(req.Slug); err == nil && existingBySlug != nil && existingBySlug.ID != id {
			c.JSON(http.StatusConflict, gin.H{"error": "标识已被其他项目使用"})
			return
		}
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

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_project", fmt.Sprintf("更新项目 #%d: %s (%s)", id, req.Name, req.Slug), fmt.Sprintf("%v", user), clientIP)

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

	// Webhook: project archived/unarchived
	go a.sendWebhookEvent("project_archived", map[string]interface{}{
		"id":       id,
		"archived": req.Archived,
	}, nil)

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
	// RBAC: verify user has access to this project
	if !a.checkProjectWritePerm(c, proj.Slug) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权查看该项目的分类"})
		return
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
	// RBAC: verify user has write permission on this project
	if !a.checkProjectWritePerm(c, proj.Slug) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权管理该项目的分类"})
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

	// RBAC: verify category belongs to a project the user can edit
	cat, err := a.DB.GetCategory(id)
	if err != nil || cat == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "分类不存在"})
		return
	}
	if !a.checkProjectWritePerm(c, cat.ProjectSlug) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权修改该项目分类"})
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

	// RBAC: verify category belongs to a project the user can edit
	cat, err := a.DB.GetCategory(id)
	if err != nil || cat == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "分类不存在"})
		return
	}
	if !a.checkProjectWritePerm(c, cat.ProjectSlug) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权删除该项目分类"})
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
