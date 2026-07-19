package routes

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"feedshit/internal/app"
	"feedshit/internal/middleware"
)

//go:embed frontend/*
var frontendFS embed.FS

// Register sets up all routes on the given Gin engine.
func Register(r *gin.Engine, application *app.App) {
	// --- Read embedded frontend files ---
	frontendSub, err := fs.Sub(frontendFS, "frontend")
	if err != nil {
		panic("Failed to get embedded frontend: " + err.Error())
	}

	// Read HTML files at startup
	indexHTML := mustReadFS(frontendSub, "index.html")
	feedbackHTML := mustReadFS(frontendSub, "feedback.html")
	loginHTML := mustReadFS(frontendSub, "login.html")
	setupHTML := mustReadFS(frontendSub, "setup.html")
	adminHTML := mustReadFS(frontendSub, "admin.html")
	trackHTML := mustReadFS(frontendSub, "track.html")

	// ========== Pre-setup whitelist ==========
	setupWhitelist := []string{
		"/health",
		"/setup",
		"/api/v1/setup/status",
		"/api/v1/setup",
		"/fb/",
		"/track",
		"/api/v1/track/",
	}

	setupGuard := func(c *gin.Context) {
		if application.IsSetupComplete() {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		for _, prefix := range setupWhitelist {
			if path == prefix || (prefix != "/health" && strings.HasPrefix(path, prefix)) {
				c.Next()
				return
			}
		}
		if strings.HasPrefix(path, "/api/") {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "系统尚未完成初始化"})
			c.Abort()
		} else {
			c.Redirect(http.StatusFound, "/setup")
			c.Abort()
		}
	}

	r.Use(setupGuard)

	// ========== Public page routes ==========

	// Deep health check — verifies DB connectivity
	r.GET("/health", application.HealthCheck)

	// Landing page
	r.GET("/", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	})

	// Setup page (always accessible via whitelist)
	r.GET("/setup", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", setupHTML)
	})

	// ========== Per-project feedback pages ==========

	r.GET("/fb/:slug", func(c *gin.Context) {
		slug := c.Param("slug")
		project, err := application.DB.GetProjectBySlug(slug)

		// If not found, check slug history for redirect
		if err != nil {
			resolved := application.DB.ResolveSlug(slug)
			if resolved != slug {
				c.Redirect(http.StatusMovedPermanently, "/fb/"+resolved)
				return
			}
			c.Data(http.StatusNotFound, "text/html; charset=utf-8", []byte(`<!DOCTYPE html><html><head><meta charset="UTF-8"><title>404</title></head><body style="font-family:sans-serif;text-align:center;padding:60px"><h1>页面不存在</h1><p>该项目不存在</p></body></html>`))
			return
		}

		// Archived projects don't accept new feedback
		if project.IsArchived {
			c.Data(http.StatusGone, "text/html; charset=utf-8", []byte(`<!DOCTYPE html><html><head><meta charset="UTF-8"><title>已归档</title></head><body style="font-family:sans-serif;text-align:center;padding:60px"><h1>项目已归档</h1><p>该项目已归档，不再接受新的反馈</p></body></html>`))
			return
		}

		if !project.IsActive {
			c.Data(http.StatusNotFound, "text/html; charset=utf-8", []byte(`<!DOCTYPE html><html><head><meta charset="UTF-8"><title>404</title></head><body style="font-family:sans-serif;text-align:center;padding:60px"><h1>页面不存在</h1><p>该项目已停用</p></body></html>`))
			return
		}
		// Fetch active categories for this project
		allCats, _ := application.DB.ListCategories(project.Slug)
		var activeCats []map[string]string
		for _, cat := range allCats {
			if cat.IsActive {
				activeCats = append(activeCats, map[string]string{"key": cat.Key, "name": cat.Name})
			}
		}
		info, _ := json.Marshal(map[string]interface{}{
			"name":        project.Name,
			"slug":        project.Slug,
			"description": project.Description,
			"form_schema": json.RawMessage(project.FormSchema),
			"categories":  activeCats,
		})
		html := strings.Replace(string(feedbackHTML), "/*__PROJECT_DATA__*/null", string(info), 1)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
	})

	// Legacy /feedback redirect
	r.GET("/feedback", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/fb/default")
	})

	// ========== Public API routes ==========

	r.GET("/api/v1/setup/status", application.SetupStatus)
	r.POST("/api/v1/setup", application.DoSetup)
	r.GET("/api/v1/projects", application.PublicListProjects)

	submit := r.Group("/api/v1/feedback")
	submit.Use(middleware.RateLimitMiddleware(application.RL))
	submit.POST("/submit", application.SubmitFeedback)

	// API Token feedback submission (external systems like CI, monitoring)
	apiSubmit := r.Group("/api/v1/external")
	apiSubmit.Use(application.APITokenAuthMiddleware())
	apiSubmit.POST("/feedback", application.SubmitFeedbackWithToken)

	// Public tracking routes (submitter self-service)
	r.GET("/track", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", trackHTML)
	})
	r.GET("/api/v1/track/feedback", application.PublicTrackFeedback)
	trackReply := r.Group("/api/v1/track")
	trackReply.Use(middleware.RateLimitMiddleware(application.RL))
	trackReply.POST("/reply", application.PublicSubmitReply)

	// ========== Admin page routes (HTML) ==========

	r.GET("/admin", func(c *gin.Context) {
		token, err := c.Cookie("admin_session")
		if err != nil || token == "" {
			c.Redirect(http.StatusFound, "/admin/login")
			return
		}
		if _, _, ok := application.SM.Validate(token); !ok {
			c.Redirect(http.StatusFound, "/admin/login")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", adminHTML)
	})

	r.GET("/admin/*path", func(c *gin.Context) {
		path := c.Param("path")

		if path == "/login" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", loginHTML)
			return
		}

		token, err := c.Cookie("admin_session")
		if err != nil || token == "" {
			c.Redirect(http.StatusFound, "/admin/login")
			return
		}
		if _, _, ok := application.SM.Validate(token); !ok {
			c.Redirect(http.StatusFound, "/admin/login")
			return
		}

		if len(path) >= 7 && path[:7] == "/files/" {
			application.AdminServeFile(c)
			return
		}

		c.Data(http.StatusOK, "text/html; charset=utf-8", adminHTML)
	})

	// ========== Admin API routes ==========

	// Public: login
	r.POST("/api/v1/admin/login", application.AdminLogin)

	// Authenticated: everything else
	adminAPI := r.Group("/api/v1/admin")
	adminAPI.Use(middleware.AuthMiddleware(application.SM))
	adminAPI.Use(middleware.CSRFMiddleware(application.SM))
	{
		// Session
		adminAPI.POST("/logout", application.AdminLogout)
		adminAPI.GET("/csrf-token", application.AdminGetCSRFToken)
		adminAPI.GET("/me", application.AdminGetCurrentUser)

		// Dashboard
		adminAPI.GET("/stats", application.AdminStats)
		adminAPI.GET("/project-stats", application.AdminProjectStats)

		// Feedbacks
		adminAPI.GET("/feedbacks", application.AdminListFeedbacks)
		adminAPI.GET("/feedbacks/export", application.AdminExportCSV)
		adminAPI.GET("/feedbacks/:id", application.AdminGetFeedback)
		adminAPI.PUT("/feedbacks/:id/status", application.AdminUpdateFeedbackStatus)
		adminAPI.PUT("/feedbacks/:id/assignee", application.AdminUpdateFeedbackAssignee)
		adminAPI.PUT("/feedbacks/:id/priority", application.AdminUpdateFeedbackPriority)
		adminAPI.POST("/feedbacks/:id/duplicate", application.AdminMarkAsDuplicate)
		adminAPI.DELETE("/feedbacks/:id/duplicate", application.AdminUnmarkDuplicate)
		adminAPI.DELETE("/feedbacks/:id", application.AdminDeleteFeedback)
		adminAPI.POST("/feedbacks/:id/notes", application.AdminAddFeedbackNote)
		adminAPI.GET("/feedbacks/:id/notes", application.AdminListFeedbackNotes)
		adminAPI.DELETE("/feedbacks/:id/notes/:noteId", application.AdminDeleteFeedbackNote)
		adminAPI.POST("/feedbacks/bulk-delete", application.AdminBulkDeleteFeedbacks)
		adminAPI.POST("/feedbacks/bulk-status", application.AdminBulkUpdateStatus)

		// Projects (editor+)
		adminAPI.GET("/projects", application.AdminListProjects)
		adminAPI.POST("/projects", middleware.RequireRole("editor"), application.AdminCreateProject)
		adminAPI.PUT("/projects/:id", middleware.RequireRole("editor"), application.AdminUpdateProject)
		adminAPI.DELETE("/projects/:id", middleware.RequireRole("admin"), application.AdminDeleteProject)

		// Admin team management (admin only)
		adminAPI.GET("/admins", middleware.RequireRole("admin"), application.AdminListAdmins)
		adminAPI.POST("/admins", middleware.RequireRole("admin"), application.AdminCreateAdmin)
		adminAPI.PUT("/admins/:id", middleware.RequireRole("admin"), application.AdminUpdateAdmin)
		adminAPI.DELETE("/admins/:id", middleware.RequireRole("admin"), application.AdminDeleteAdmin)

		// Member grants — fine-grained RBAC (admin only)
		adminAPI.GET("/admins/:id/grants", middleware.RequireRole("admin"), application.AdminGetMemberGrants)
		adminAPI.PUT("/admins/:id/grants", middleware.RequireRole("admin"), application.AdminSetMemberGrants)
		adminAPI.DELETE("/admins/:id/grants/:grantId", middleware.RequireRole("admin"), application.AdminDeleteMemberGrant)

		// Category management (editor+)
		adminAPI.GET("/projects/:id/categories", middleware.RequireRole("editor"), application.AdminListCategories)
		adminAPI.POST("/projects/:id/categories", middleware.RequireRole("editor"), application.AdminCreateCategory)
		adminAPI.PUT("/categories/:id", middleware.RequireRole("editor"), application.AdminUpdateCategory)
		adminAPI.DELETE("/categories/:id", middleware.RequireRole("admin"), application.AdminDeleteCategory)
		adminAPI.PATCH("/feedbacks/:id/category", middleware.RequireRole("editor"), application.AdminUpdateFeedbackCategory)
		adminAPI.POST("/feedbacks/bulk-category", middleware.RequireRole("editor"), application.AdminBulkUpdateCategory)

		// Project archive (admin only)
		adminAPI.POST("/projects/:id/archive", middleware.RequireRole("admin"), application.AdminArchiveProject)

		// Audit logs
		adminAPI.GET("/audit-logs", application.AdminListAuditLogs)

		// Chart data
		adminAPI.GET("/chart-data", application.AdminChartData)

		// Backup
		adminAPI.POST("/backup", application.AdminBackup)

		// API Token management (admin only)
		adminAPI.GET("/api-tokens", middleware.RequireRole("admin"), application.AdminListAPITokens)
		adminAPI.POST("/api-tokens", middleware.RequireRole("admin"), application.AdminCreateAPIToken)
		adminAPI.PUT("/api-tokens/:id", middleware.RequireRole("admin"), application.AdminUpdateAPIToken)
		adminAPI.DELETE("/api-tokens/:id", middleware.RequireRole("admin"), application.AdminDeleteAPIToken)

		// Bulk operations (editor+)
		adminAPI.POST("/feedbacks/bulk-tags", middleware.RequireRole("editor"), application.AdminBulkUpdateTags)
		adminAPI.POST("/feedbacks/bulk-assignee", middleware.RequireRole("editor"), application.AdminBulkUpdateAssignee)
		adminAPI.POST("/feedbacks/bulk-priority", middleware.RequireRole("editor"), application.AdminBulkUpdatePriority)

		// CSV Import (editor+)
		adminAPI.POST("/import/csv", middleware.RequireRole("editor"), application.AdminImportCSV)

		// Data archive & cleanup (admin only)
		adminAPI.POST("/archive", middleware.RequireRole("admin"), application.AdminArchiveOldFeedbacks)
		adminAPI.POST("/prune-backups", middleware.RequireRole("admin"), application.AdminPruneOldBackups)

		// Email template (admin only)
		adminAPI.GET("/config/email-template", middleware.RequireRole("admin"), application.AdminGetEmailTemplate)
		adminAPI.PUT("/config/email-template", middleware.RequireRole("admin"), application.AdminUpdateEmailTemplate)

		// Config sections
		adminAPI.GET("/config/email", middleware.RequireRole("admin"), application.AdminGetEmailConfig)
		adminAPI.PUT("/config/email", middleware.RequireRole("admin"), application.AdminUpdateEmailConfig)
		adminAPI.GET("/config/account", middleware.RequireRole("admin"), application.AdminGetAccountConfig)
		adminAPI.PUT("/config/account", middleware.RequireRole("admin"), application.AdminUpdateAccount)
		adminAPI.GET("/config/system", middleware.RequireRole("admin"), application.AdminGetSystemConfig)
		adminAPI.PUT("/config/system", middleware.RequireRole("admin"), application.AdminUpdateSystemConfig)

		// Legacy config (backward compat)
		adminAPI.GET("/config", middleware.RequireRole("admin"), application.AdminGetConfig)
		adminAPI.PUT("/config", middleware.RequireRole("admin"), application.AdminUpdateConfig)
	}
}

func mustReadFS(fsys fs.FS, name string) []byte {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		panic("Failed to read embedded file " + name + ": " + err.Error())
	}
	return data
}
