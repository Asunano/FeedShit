package app

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"feedshit/internal/database"
	"feedshit/internal/middleware"
)

// faqMaxResults reads the public search result cap from FAQ_MAX_RESULTS.
// Default 5, clamped to the inclusive range [1, 50].
func faqMaxResults() int {
	n, err := strconv.Atoi(os.Getenv("FAQ_MAX_RESULTS"))
	if err != nil || n < 1 {
		n = 5
	}
	if n > 50 {
		n = 50
	}
	return n
}

// PublicSearchFAQ serves the self-service knowledge base to anonymous submitters.
// GET /api/v1/faq?q=<keyword>&project=<slug>
// Empty q or project yields {faqs:[]} with no error and no cross-project leak.
// Results are mapped to PublicFAQ ({id,question,answer}) only.
func (a *App) PublicSearchFAQ(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	project := strings.TrimSpace(c.Query("project"))
	if q == "" || project == "" {
		c.JSON(http.StatusOK, gin.H{"faqs": []database.PublicFAQ{}})
		return
	}
	// Guard against pathological input: cap keyword length at 100 chars.
	runes := []rune(q)
	if len(runes) > 100 {
		q = string(runes[:100])
	}
	// Parameterized LIKE: wrap in %q% in Go, never concatenate into SQL.
	wrapped := "%" + q + "%"
	faqs, err := a.DB.SearchFAQs(project, wrapped, faqMaxResults())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "检索失败"})
		return
	}
	if faqs == nil {
		faqs = []database.FAQ{}
	}
	public := make([]database.PublicFAQ, 0, len(faqs))
	for _, f := range faqs {
		public = append(public, database.PublicFAQ{
			ID:       f.ID,
			Question: f.Question,
			Answer:   f.Answer,
		})
	}
	c.JSON(http.StatusOK, gin.H{"faqs": public})
}

// AdminListFAQs lists every FAQ of a project (including inactive ones).
// GET /api/v1/admin/projects/:slug/faqs
func (a *App) AdminListFAQs(c *gin.Context) {
	slug := c.Param("slug")
	proj, projErr := a.DB.GetProjectBySlug(slug)
	if projErr != nil || proj == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return
	}
	faqs, err := a.DB.ListFAQs(slug)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if faqs == nil {
		faqs = []database.FAQ{}
	}
	c.JSON(http.StatusOK, gin.H{"faqs": faqs})
}

// AdminCreateFAQ creates a new FAQ for a project.
// POST /api/v1/admin/projects/:slug/faqs
func (a *App) AdminCreateFAQ(c *gin.Context) {
	slug := c.Param("slug")
	proj, projErr := a.DB.GetProjectBySlug(slug)
	if projErr != nil || proj == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return
	}
	var req struct {
		Question  string `json:"question"`
		Answer    string `json:"answer"`
		SortOrder int    `json:"sort_order"`
		IsActive  *bool  `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "问题不能为空"})
		return
	}
	// Duplicate detection within the same project.
	existing, _ := a.DB.GetFAQByQuestion(slug, req.Question)
	if existing != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "该问题已存在"})
		return
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	faqID, err := a.DB.CreateFAQ(slug, req.Question, req.Answer, req.SortOrder, isActive)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("create_faq", "创建 FAQ："+req.Question, fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusCreated, gin.H{"id": faqID, "question": req.Question})
}

// AdminUpdateFAQ updates an existing FAQ, constrained to its owning project.
// PUT /api/v1/admin/projects/:slug/faqs/:id
func (a *App) AdminUpdateFAQ(c *gin.Context) {
	slug := c.Param("slug")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	var req struct {
		Question  string `json:"question"`
		Answer    string `json:"answer"`
		SortOrder int    `json:"sort_order"`
		IsActive  *bool  `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "问题不能为空"})
		return
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	if err := a.DB.UpdateFAQ(id, slug, req.Question, req.Answer, req.SortOrder, isActive); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "FAQ 不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_faq", "更新 FAQ #"+strconv.FormatInt(id, 10), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

// AdminDeleteFAQ hard-deletes a FAQ, constrained to its owning project.
// DELETE /api/v1/admin/projects/:slug/faqs/:id
func (a *App) AdminDeleteFAQ(c *gin.Context) {
	slug := c.Param("slug")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	if err := a.DB.DeleteFAQ(id, slug); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "FAQ 不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_faq", "删除 FAQ #"+strconv.FormatInt(id, 10), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}
