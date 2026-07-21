package routes

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"feedshit/internal/app"
	"feedshit/internal/middleware"
)

// nonceCtxKey is the gin context key for the CSP nonce value.
const nonceCtxKey = "csp_nonce"

// nonceMiddleware generates a unique CSP nonce per request and stores it in context.
func nonceMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			// Fallback: use a static nonce if rand fails (extremely unlikely)
			c.Set(nonceCtxKey, "fallback")
			c.Next()
			return
		}
		c.Set(nonceCtxKey, base64.StdEncoding.EncodeToString(b))
		c.Next()
	}
}

//go:embed frontend/*
var frontendFS embed.FS

// Register sets up all routes on the given Gin engine.
func Register(r *gin.Engine, application *app.App) {
	// --- Read embedded frontend files ---
	frontendSub, err := fs.Sub(frontendFS, "frontend")
	if err != nil {
		panic("Failed to get embedded frontend: " + err.Error())
	}

	// 共享静态资源（设计系统 tokens / 组件 / 统一主题管理），随 frontend/* 一并嵌入，无构建步骤
	sharedSub, err := fs.Sub(frontendSub, "shared")
	if err != nil {
		panic("Failed to get embedded shared assets: " + err.Error())
	}
	sharedFS := http.FileServer(http.FS(sharedSub))
	r.GET("/shared/*filepath", func(c *gin.Context) {
		// 去掉前缀后交由以 shared/ 为根的文件服务器处理
		c.Request.URL.Path = strings.TrimPrefix(c.Request.URL.Path, "/shared")
		sharedFS.ServeHTTP(c.Writer, c.Request)
	})

	// Parse all HTML page templates once at startup. The shared layout
	// (frontend/layouts/base.html) defines "chrometop"/"chromebot"; each page under
	// frontend/pages/*.html references it via {{template "chrometop" .}} /
	// {{template "chromebot" .}} and gets the per-request CSP nonce via {{.Nonce}}.
	// This IS the unified container: new pages write only their own content + a thin
	// <script nonce> block — never the theme button, header, or footer boilerplate.
	tpl := template.Must(template.ParseFS(frontendFS, "frontend/layouts/base.html", "frontend/pages/*.html"))

	// ========== Pre-setup whitelist ==========
	setupWhitelist := []string{
		"/health",
		"/setup",
		"/shared",
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

	// ========== CSP nonce middleware ==========
	r.Use(nonceMiddleware())

	// ========== Security headers ==========

	// Content-Security-Policy: restricts script/style sources to mitigate XSS.
	r.Use(func(c *gin.Context) {
		nonce, _ := c.Get(nonceCtxKey)
		nonceVal, _ := nonce.(string)
		c.Header("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'nonce-"+nonceVal+"'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self'; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'")
	})

	// ========== Public page routes ==========

	// Deep health check — verifies DB connectivity
	r.GET("/health", application.HealthCheck)

	// Landing page
	r.GET("/", func(c *gin.Context) {
		serveTemplate(c, tpl, "index.html", PageData{Nav: "", Nonce: nonceOf(c)})
	})

	// Dedicated public project list page
	r.GET("/projects", func(c *gin.Context) {
		serveTemplate(c, tpl, "projects.html", PageData{Nav: "", Nonce: nonceOf(c)})
	})

	// Setup page (always accessible via whitelist)
	r.GET("/setup", func(c *gin.Context) {
		serveTemplate(c, tpl, "setup.html", PageData{Nav: "", Nonce: nonceOf(c)})
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
			"name":         project.Name,
			"slug":         project.Slug,
			"description":  project.Description,
			"announcement": json.RawMessage(announcementOrDefault(project.Announcement)),
			"form_schema":  json.RawMessage(formSchemaOrDefault(project.FormSchema)),
			"categories":   activeCats,
		})
		// Render the unified template (nonce via {{.Nonce}}) and inject project data.
		rendered, err := executePage(tpl, "feedback.html", PageData{Nav: "", Nonce: nonceOf(c)})
		if err != nil {
			c.Data(http.StatusInternalServerError, "text/plain; charset=utf-8", []byte("template render error"))
			return
		}
		// Inject project JSON. Use a plain token placeholder (NOT a /* */ comment):
		// html/template strips JS block comments, so a comment-based marker would
		// be removed at render time and this replace would silently no-op.
		// Escape '<' so a project name/description containing "</script>" cannot
		// break out of the inline script block.
		projectJSON := strings.ReplaceAll(string(info), "<", "\\u003c")
		rendered = strings.Replace(rendered, "__PROJECT_DATA__", projectJSON, 1)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(rendered))
	})

	// Legacy /feedback redirect
	r.GET("/feedback", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/fb/default")
	})

	// Public roadmap board (no login required)
	r.GET("/p/:slug/roadmap", func(c *gin.Context) {
		serveTemplate(c, tpl, "roadmap.html", PageData{Nav: "", Nonce: nonceOf(c)})
	})

	// ========== Public API routes ==========

	r.GET("/api/v1/setup/status", application.SetupStatus)
	r.POST("/api/v1/setup", application.DoSetup)
	r.GET("/api/v1/projects", application.PublicListProjects)
	r.GET("/api/v1/announcement", application.PublicGetAnnouncement)
	r.GET("/api/v1/roadmap", application.PublicRoadmap)

	submit := r.Group("/api/v1/feedback")
	submit.Use(middleware.RateLimitMiddleware(application.RL))
	submit.POST("/submit", application.SubmitFeedback)
	submit.POST("/:id/vote", application.PublicVoteFeedback)
	submit.GET("/check-duplicate", application.PublicCheckDuplicate)

	// Public FAQ self-service search (rate-limited, no login required)
	faqPub := r.Group("/api/v1")
	faqPub.Use(middleware.RateLimitMiddleware(application.RL))
	faqPub.GET("/faq", application.PublicSearchFAQ)

	// Invitation registration page — rendered by the unified template, with the
	// invite token injected in place of INVITE_TOKEN_PLACEHOLDER.
	r.GET("/invite/:token", func(c *gin.Context) {
		token := c.Param("token")
		if _, err := application.DB.ValidateInvitation(token); err != nil {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<html><body style="font-family:sans-serif;padding:40px;text-align:center"><h2>邀请链接无效或已过期</h2><p>请联系管理员获取新的邀请链接。</p></body></html>`))
			return
		}
		html, err := executePage(tpl, "register.html", PageData{Nav: "", Nonce: nonceOf(c)})
		if err != nil {
			c.Data(http.StatusInternalServerError, "text/plain; charset=utf-8", []byte("template render error"))
			return
		}
		html = strings.ReplaceAll(html, "INVITE_TOKEN_PLACEHOLDER", token)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
	})
	r.POST("/api/v1/invite/:token/register", application.PublicRegister)

	// API Token feedback submission (external systems like CI, monitoring)
	apiSubmit := r.Group("/api/v1/external")
	apiSubmit.Use(middleware.RateLimitMiddleware(application.RL))
	apiSubmit.Use(application.APITokenAuthMiddleware())
	apiSubmit.POST("/feedback", application.SubmitFeedbackWithToken)

	// Public tracking routes (submitter self-service)
	r.GET("/track", func(c *gin.Context) {
		serveTemplate(c, tpl, "track.html", PageData{Nav: "", Nonce: nonceOf(c)})
	})
	r.GET("/api/v1/track/feedback", application.PublicTrackFeedback)
	trackReply := r.Group("/api/v1/track")
	trackReply.Use(middleware.RateLimitMiddleware(application.RL))
	trackReply.GET("/by-email", application.PublicListByEmail)
	trackReply.POST("/reply", application.PublicSubmitReply)
	trackReply.POST("/:token/rating", application.PublicSubmitRating)
	trackReply.POST("/:token/need-help", application.PublicNeedHelp)

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
		serveTemplate(c, tpl, "admin.html", PageData{Nav: "admin", Nonce: nonceOf(c)})
	})

	r.GET("/admin/*path", func(c *gin.Context) {
		path := c.Param("path")

		if path == "/login" {
			serveTemplate(c, tpl, "login.html", PageData{Nav: "", Nonce: nonceOf(c)})
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

		serveTemplate(c, tpl, "admin.html", PageData{Nav: "admin", Nonce: nonceOf(c)})
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
		adminAPI.POST("/csrf-token", application.AdminGetCSRFToken)
		adminAPI.GET("/me", application.AdminGetCurrentUser)

		// Dashboard
		adminAPI.GET("/stats", application.AdminStats)
		adminAPI.GET("/project-stats", application.AdminProjectStats)

		// Feedbacks
		adminAPI.GET("/feedbacks", application.AdminListFeedbacks)
		adminAPI.GET("/feedbacks/export", application.AdminExportCSV)
		adminAPI.GET("/feedbacks/:id", application.AdminGetFeedback)
		adminAPI.PUT("/feedbacks/:id/status", application.AdminUpdateFeedbackStatus)
		adminAPI.POST("/feedbacks/:id/rating-invite", application.AdminTriggerRatingInvite)
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
		adminAPI.PUT("/feedbacks/:id/roadmap", application.AdminSetRoadmap)

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
		adminAPI.PUT("/admins/:id/reset-password", middleware.RequireRole("admin"), application.AdminResetPassword)

		// Member grants — fine-grained RBAC (admin only)
		adminAPI.GET("/admins/:id/grants", middleware.RequireRole("admin"), application.AdminGetMemberGrants)
		adminAPI.PUT("/admins/:id/grants", middleware.RequireRole("admin"), application.AdminSetMemberGrants)
		adminAPI.DELETE("/admins/:id/grants/:grantId", middleware.RequireRole("admin"), application.AdminDeleteMemberGrant)

		// Invitations
		adminAPI.POST("/invitations", middleware.RequireRole("admin"), application.AdminCreateInvitation)
		adminAPI.GET("/invitations", middleware.RequireRole("admin"), application.AdminListInvitations)

		// Category management (editor+)
		adminAPI.GET("/projects/:id/categories", middleware.RequireRole("editor"), application.AdminListCategories)
		adminAPI.POST("/projects/:id/categories", middleware.RequireRole("editor"), application.AdminCreateCategory)
		adminAPI.PUT("/categories/:id", middleware.RequireRole("editor"), application.AdminUpdateCategory)
		adminAPI.DELETE("/categories/:id", middleware.RequireRole("admin"), application.AdminDeleteCategory)
		adminAPI.PATCH("/feedbacks/:id/category", middleware.RequireRole("editor"), application.AdminUpdateFeedbackCategory)
		adminAPI.POST("/feedbacks/bulk-category", middleware.RequireRole("editor"), application.AdminBulkUpdateCategory)

		// FAQ self-service knowledge base (editor+)
		adminAPI.GET("/projects/:id/faqs", middleware.RequireRole("editor"), application.AdminListFAQs)
		adminAPI.POST("/projects/:id/faqs", middleware.RequireRole("editor"), application.AdminCreateFAQ)
		adminAPI.PUT("/projects/:id/faqs/:faqId", middleware.RequireRole("editor"), application.AdminUpdateFAQ)
		adminAPI.DELETE("/projects/:id/faqs/:faqId", middleware.RequireRole("admin"), application.AdminDeleteFAQ)

		// Duplicate detection (editor+): candidate similar feedback for a given feedback
		adminAPI.GET("/feedbacks/:id/similar", middleware.RequireRole("editor"), application.AdminSimilarFeedbacks)

		// Project archive (admin only)
		adminAPI.POST("/projects/:id/archive", middleware.RequireRole("admin"), application.AdminArchiveProject)

		// Audit logs
		adminAPI.GET("/audit-logs", application.AdminListAuditLogs)

		// Chart data
		adminAPI.GET("/chart-data", application.AdminChartData)

		// Backup
		adminAPI.POST("/backup", application.AdminBackup)
		adminAPI.GET("/backup/download", application.AdminBackupDownload)
		adminAPI.GET("/backups", application.AdminListBackups)

		// API Token management (admin only)
		adminAPI.GET("/api-tokens", middleware.RequireRole("admin"), application.AdminListAPITokens)
		adminAPI.POST("/api-tokens", middleware.RequireRole("admin"), application.AdminCreateAPIToken)
		adminAPI.PUT("/api-tokens/:id", middleware.RequireRole("admin"), application.AdminUpdateAPIToken)
		adminAPI.DELETE("/api-tokens/:id", middleware.RequireRole("admin"), application.AdminDeleteAPIToken)

		// Webhook subscriptions (admin only)
		adminAPI.GET("/webhooks", middleware.RequireRole("admin"), application.AdminListWebhookSubscriptions)
		adminAPI.POST("/webhooks", middleware.RequireRole("admin"), application.AdminCreateWebhookSubscription)
		adminAPI.PUT("/webhooks/:id", middleware.RequireRole("admin"), application.AdminUpdateWebhookSubscription)
		adminAPI.DELETE("/webhooks/:id", middleware.RequireRole("admin"), application.AdminDeleteWebhookSubscription)

		// Bulk operations (editor+)
		adminAPI.POST("/feedbacks/bulk-tags", middleware.RequireRole("editor"), application.AdminBulkUpdateTags)
		adminAPI.POST("/feedbacks/bulk-assignee", middleware.RequireRole("editor"), application.AdminBulkUpdateAssignee)
		adminAPI.POST("/feedbacks/bulk-priority", middleware.RequireRole("editor"), application.AdminBulkUpdatePriority)

		// CSV Import (editor+)
		adminAPI.POST("/import/csv", middleware.RequireRole("editor"), application.AdminImportCSV)
		adminAPI.POST("/import/json", middleware.RequireRole("editor"), application.AdminImportJSON)

		// Data archive & cleanup (admin only)
		adminAPI.POST("/archive", middleware.RequireRole("admin"), application.AdminArchiveOldFeedbacks)
		// Tag autocomplete
		adminAPI.GET("/tags", application.AdminGetTags)
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

		// Global announcement (admin only)
		adminAPI.GET("/config/announcement", middleware.RequireRole("admin"), application.AdminGetAnnouncement)
		adminAPI.PUT("/config/announcement", middleware.RequireRole("admin"), application.AdminUpdateAnnouncement)

		// Legacy config (backward compat)
		adminAPI.GET("/config", middleware.RequireRole("admin"), application.AdminGetConfig)
		adminAPI.PUT("/config", middleware.RequireRole("admin"), application.AdminUpdateConfig)
	}
}

// defaultFormSchemaJSON is the schema used for projects that have no
// form_schema configured. Every field is schema-driven (system fields are
// mapped via "sys"), so the public feedback page never falls back to hardcoded
// template text. Admins can customize these fields (labels, placeholders,
// required) from the admin "自定义表单字段" editor.
const defaultFormSchemaJSON = `[
  {"key":"title","name":"title","label":"反馈标题","type":"text","required":true,"sys":"title","placeholder":"请输入反馈标题"},
  {"key":"description","name":"description","label":"详细描述","type":"textarea","sys":"description","placeholder":"请描述您遇到的问题或建议","rows":5},
  {"key":"category","name":"category","label":"分类","type":"select","sys":"category"},
  {"key":"notify","name":"notify","label":"接收反馈处理通知","type":"checkbox","sys":"notify"},
  {"key":"images","name":"images","label":"截图上传","type":"image","sys":"images","multiple":true},
  {"key":"files","name":"files","label":"日志 / 附件","type":"file","sys":"files","multiple":true}
]`

// formSchemaOrDefault returns a valid JSON array. If s is empty (the project has
// no configured schema), it returns the default schema instead of "[]" so the
// public page always renders a complete, backend-operated form.
func formSchemaOrDefault(s string) string {
	t := strings.TrimSpace(s)
	if t == "" || t == "null" || t == "[]" {
		return defaultFormSchemaJSON
	}
	return s
}

// announcementOrDefault returns a valid announcement JSON object. An empty or
// invalid stored value yields {"enabled":false} so the feedback page renders no
// project banner. The payload shape matches the public announcement struct
// (enabled/content_type/content/level/dismissible).
func announcementOrDefault(s string) string {
	t := strings.TrimSpace(s)
	if t == "" || t == "null" {
		return `{"enabled":false}`
	}
	return s
}

// PageData is the data model passed to every page template. It carries the
// per-request CSP nonce and an optional Nav flag. Nav == "admin" switches the
// shared layout (chrometop) from the floating theme toggle to the full admin
// chrome (header + tab nav + logout button).
type PageData struct {
	Title string
	Nav   string
	Nonce string
}

// nonceOf returns the per-request CSP nonce, or "" if unavailable.
func nonceOf(c *gin.Context) string {
	if v, ok := c.Get(nonceCtxKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// executePage renders a named page template (which includes the shared
// chrometop/chromebot layout via {{template}}) to a string.
func executePage(tpl *template.Template, name string, data PageData) (string, error) {
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// serveTemplate renders a page template and writes it as text/html.
func serveTemplate(c *gin.Context, tpl *template.Template, name string, data PageData) {
	html, err := executePage(tpl, name, data)
	if err != nil {
		c.Data(http.StatusInternalServerError, "text/plain; charset=utf-8", []byte("template render error: "+err.Error()))
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}
