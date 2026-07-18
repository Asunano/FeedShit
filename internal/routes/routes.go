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

	// ========== Pre-setup whitelist ==========
	// Only these paths are accessible before setup is complete.
	// New routes are automatically blocked — no per-route guard needed.
	setupWhitelist := []string{
		"/health",
		"/setup",
		"/api/v1/setup/status",
		"/api/v1/setup",
		"/fb/",
	}

	setupGuard := func(c *gin.Context) {
		if application.IsSetupComplete() {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		// Whitelist: allow specific paths even before setup
		for _, prefix := range setupWhitelist {
			if path == prefix || (prefix != "/health" && strings.HasPrefix(path, prefix)) {
				c.Next()
				return
			}
		}
		// Block everything else
		if strings.HasPrefix(path, "/api/") {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "系统尚未完成初始化"})
			c.Abort()
		} else {
			c.Redirect(http.StatusFound, "/setup")
			c.Abort()
		}
	}

	// Apply pre-setup guard globally
	r.Use(setupGuard)

	// ========== Public page routes ==========

	// Health check for container orchestration
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Landing page
	r.GET("/", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	})

	// Setup page (always accessible via whitelist)
	r.GET("/setup", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", setupHTML)
	})

	// ========== Per-project feedback pages ==========

	// Dedicated feedback page per project: /fb/{slug}
	r.GET("/fb/:slug", func(c *gin.Context) {
		slug := c.Param("slug")
		project, err := application.DB.GetProjectBySlug(slug)
		if err != nil || !project.IsActive {
			c.Data(http.StatusNotFound, "text/html; charset=utf-8", []byte(`<!DOCTYPE html><html><head><meta charset="UTF-8"><title>404</title></head><body style="font-family:sans-serif;text-align:center;padding:60px"><h1>页面不存在</h1><p>该项目不存在或已停用</p></body></html>`))
			return
		}
		info, _ := json.Marshal(map[string]interface{}{
			"name":        project.Name,
			"slug":        project.Slug,
			"description": project.Description,
			"form_schema": json.RawMessage(project.FormSchema),
		})
		html := strings.Replace(string(feedbackHTML), "/*__PROJECT_DATA__*/null", string(info), 1)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
	})

	// Legacy /feedback redirect
	r.GET("/feedback", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/fb/default")
	})

	// ========== Public API routes ==========

	// Setup (always accessible via whitelist)
	r.GET("/api/v1/setup/status", application.SetupStatus)
	r.POST("/api/v1/setup", application.DoSetup)

	// Public projects list (only after setup)
	r.GET("/api/v1/projects", application.PublicListProjects)

	// Feedback submission (with rate limit)
	submit := r.Group("/api/v1/feedback")
	submit.Use(middleware.RateLimitMiddleware(application.RL))
	submit.POST("/submit", application.SubmitFeedback)

	// ========== Admin page routes (HTML) ==========

	// Exact /admin route
	r.GET("/admin", func(c *gin.Context) {
		token, err := c.Cookie("admin_session")
		if err != nil || token == "" {
			c.Redirect(http.StatusFound, "/admin/login")
			return
		}
		if _, ok := application.SM.Validate(token); !ok {
			c.Redirect(http.StatusFound, "/admin/login")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", adminHTML)
	})

	// Catch-all for /admin/* paths
	r.GET("/admin/*path", func(c *gin.Context) {
		path := c.Param("path")

		// Login page — no auth required
		if path == "/login" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", loginHTML)
			return
		}

		// Auth check
		token, err := c.Cookie("admin_session")
		if err != nil || token == "" {
			c.Redirect(http.StatusFound, "/admin/login")
			return
		}
		if _, ok := application.SM.Validate(token); !ok {
			c.Redirect(http.StatusFound, "/admin/login")
			return
		}

		// Secure file serving: /admin/files/*filepath
		if len(path) >= 7 && path[:7] == "/files/" {
			application.AdminServeFile(c)
			return
		}

		// Default: serve admin SPA
		c.Data(http.StatusOK, "text/html; charset=utf-8", adminHTML)
	})

	// ========== Admin API routes ==========

	// Public: login
	r.POST("/api/v1/admin/login", application.AdminLogin)

	// Authenticated: everything else
	adminAPI := r.Group("/api/v1/admin")
	adminAPI.Use(middleware.AuthMiddleware(application.SM))
	{
		// Session
		adminAPI.POST("/logout", application.AdminLogout)

		// Dashboard
		adminAPI.GET("/stats", application.AdminStats)
		adminAPI.GET("/project-stats", application.AdminProjectStats)

		// Feedbacks
		adminAPI.GET("/feedbacks", application.AdminListFeedbacks)
		adminAPI.GET("/feedbacks/:id", application.AdminGetFeedback)
		adminAPI.DELETE("/feedbacks/:id", application.AdminDeleteFeedback)
		adminAPI.GET("/feedbacks/export", application.AdminExportCSV)

		// Projects
		adminAPI.GET("/projects", application.AdminListProjects)
		adminAPI.POST("/projects", application.AdminCreateProject)
		adminAPI.PUT("/projects/:id", application.AdminUpdateProject)
		adminAPI.DELETE("/projects/:id", application.AdminDeleteProject)

		// Config sections
		adminAPI.GET("/config/email", application.AdminGetEmailConfig)
		adminAPI.PUT("/config/email", application.AdminUpdateEmailConfig)
		adminAPI.GET("/config/account", application.AdminGetAccountConfig)
		adminAPI.PUT("/config/account", application.AdminUpdateAccount)
		adminAPI.GET("/config/system", application.AdminGetSystemConfig)
		adminAPI.PUT("/config/system", application.AdminUpdateSystemConfig)

		// Legacy config (backward compat)
		adminAPI.GET("/config", application.AdminGetConfig)
		adminAPI.PUT("/config", application.AdminUpdateConfig)
	}
}

func mustReadFS(fsys fs.FS, name string) []byte {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		panic("Failed to read embedded file " + name + ": " + err.Error())
	}
	return data
}
