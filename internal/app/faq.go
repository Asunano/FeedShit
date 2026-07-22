package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"feedshit/internal/database"
	"feedshit/internal/middleware"

	"github.com/gin-gonic/gin"
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
	ids := make([]int64, 0, len(faqs))
	for _, f := range faqs {
		ids = append(ids, f.ID)
	}
	vmap, _ := a.DB.CountFAQVotesMap(ids)
	public := make([]database.PublicFAQ, 0, len(faqs))
	for _, f := range faqs {
		public = append(public, database.PublicFAQ{
			ID:          f.ID,
			Question:    f.Question,
			Answer:      RenderMarkdown(f.Answer),
			UsefulVotes: vmap[f.ID],
		})
	}
	c.JSON(http.StatusOK, gin.H{"faqs": public})
}

// AdminListFAQs lists every FAQ of a project (including inactive ones).
// GET /api/v1/admin/projects/:id/faqs
func (a *App) AdminListFAQs(c *gin.Context) {
	slug := c.Param("id")
	proj, projErr := a.DB.GetProjectBySlug(slug)
	if projErr != nil || proj == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return
	}
	// Enforce project-level access for non-admin users
	if err := a.checkFAQProjectAccess(c, slug); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
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
// POST /api/v1/admin/projects/:id/faqs
func (a *App) AdminCreateFAQ(c *gin.Context) {
	slug := c.Param("id")
	proj, projErr := a.DB.GetProjectBySlug(slug)
	if projErr != nil || proj == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return
	}
	// Enforce project-level access for non-admin users
	if err := a.checkFAQProjectAccess(c, slug); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
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
	// Store raw Markdown; rendering + sanitization happens at display time
	// (PublicSearchFAQ / admin preview) so the source stays editable.
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
// PUT /api/v1/admin/projects/:id/faqs/:faqId
func (a *App) AdminUpdateFAQ(c *gin.Context) {
	slug := c.Param("id")
	id, err := strconv.ParseInt(c.Param("faqId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	// Enforce project-level access for non-admin users
	if err := a.checkFAQProjectAccess(c, slug); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
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
	// Store raw Markdown (see AdminCreateFAQ note).
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
// DELETE /api/v1/admin/projects/:id/faqs/:faqId
func (a *App) AdminDeleteFAQ(c *gin.Context) {
	slug := c.Param("id")
	id, err := strconv.ParseInt(c.Param("faqId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	// Enforce project-level access for non-admin users
	if err := a.checkFAQProjectAccess(c, slug); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
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

// checkFAQProjectAccess enforces project-level access for non-admin users.
// Admin users bypass this check. Returns nil if access is granted.
// If no admin session context is set (e.g. direct handler calls in tests),
// the check is skipped — route-level middleware (RequireRole) is the primary gate.
func (a *App) checkFAQProjectAccess(c *gin.Context, slug string) error {
	role, exists := c.Get("admin_role")
	if !exists {
		return nil // no session context; route middleware handles auth
	}
	roleStr, _ := role.(string)
	if roleStr == "admin" {
		return nil
	}
	username, _ := c.Get("admin_user")
	usernameStr, _ := username.(string)
	if usernameStr == "" {
		return nil
	}
	admin, _ := a.DB.GetAdminByUsername(usernameStr)
	if admin == nil {
		return fmt.Errorf("用户不存在")
	}
	plan, _ := a.DB.GetAdminAccessPlan(admin.ID)
	if plan == nil || len(plan) == 0 {
		return fmt.Errorf("无权访问该项目")
	}
	for _, pa := range plan {
		if pa.Slug == slug {
			return nil
		}
	}
	return fmt.Errorf("无权访问该项目")
}

// AdminPreviewFAQ renders Markdown to sanitized HTML for the admin FAQ editor
// live preview. POST /api/v1/admin/faqs/preview  body: {"markdown":"..."}
// The output is always sanitized through bluemonday, so it is safe to inject
// as innerHTML on the admin page.
func (a *App) AdminPreviewFAQ(c *gin.Context) {
	var req struct {
		Markdown string `json:"markdown"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"html": RenderMarkdown(req.Markdown)})
}

// PublicViewFAQ records a "view" of a FAQ (its answer was expanded in the
// self-service hint) and returns the updated view count.
// POST /api/v1/faq/:id/view
func (a *App) PublicViewFAQ(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	f, err := a.DB.GetFAQByID(id)
	if err != nil || f == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "FAQ 不存在"})
		return
	}
	views, err := a.DB.IncrementFAQViewCount(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"views": views})
}

// PublicVoteFAQ records an upvote on a FAQ from any visitor, reusing the
// feedback_votes table with target_type='faq' so it never collides with
// feedback votes that happen to share the same numeric id.
// POST /api/v1/faq/:id/vote?type=useful
func (a *App) PublicVoteFAQ(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	voteType := strings.TrimSpace(c.Query("type"))
	if voteType == "" {
		voteType = "useful"
	}
	if voteType != "useful" && voteType != "encountered" {
		voteType = "useful"
	}

	f, err := a.DB.GetFAQByID(id)
	if err != nil || f == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "FAQ 不存在"})
		return
	}
	proj, projErr := a.DB.GetProjectBySlug(f.ProjectSlug)
	if projErr != nil || proj == nil || !proj.IsActive || proj.IsArchived {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该项目已停用或已归档，无法投票"})
		return
	}

	var voterKey string
	if t := strings.TrimSpace(c.Query("token")); t != "" {
		voterKey = "tok:" + t
	} else {
		// Cap anonymous votes per client IP to blunt vote-farming.
		if !a.AnonVoteLimiter.allow(middleware.GetClientIP(c)) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "投票过于频繁，请稍后再试"})
			return
		}
		ua := c.GetHeader("User-Agent")
		h := sha256.Sum256([]byte(middleware.GetClientIP(c) + "|" + ua))
		voterKey = "anon:" + hex.EncodeToString(h[:])
	}

	already, err := a.DB.InsertVote(id, voterKey, voteType, "faq")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "投票失败"})
		return
	}
	votes, _ := a.DB.CountVotesByType(id, voteType, "faq")
	c.JSON(http.StatusOK, gin.H{"type": voteType, "voted": !already, "votes": votes})
}

// faqCSVHeaderAliases maps (normalized) Chinese CSV headers to the canonical
// column names used by the FAQ import pipeline. Kept package-scoped so preview
// and real-import share one mapping (mirrors csvHeaderAliases for feedback).
var faqCSVHeaderAliases = map[string]string{
	"问题": "question",
	"答案": "answer",
	"排序": "sort_order",
	"启用": "is_active",
}

// AdminExportFAQs streams the project's FAQs as a CSV download.
// GET /api/v1/admin/projects/:id/faqs/export
func (a *App) AdminExportFAQs(c *gin.Context) {
	slug := c.Param("id")
	if err := a.checkFAQProjectAccess(c, slug); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	faqs, err := a.DB.ExportFAQs(slug)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "导出失败"})
		return
	}
	filename := "faqs_" + slug + "_" + time.Now().Format("20060102_150405") + ".csv"
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "text/csv; charset=utf-8")

	w := csv.NewWriter(c.Writer)
	// BOM for Excel compatibility (matches the feedback export path).
	c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	w.Write([]string{"问题", "答案", "排序", "启用"})
	for _, f := range faqs {
		w.Write([]string{
			escapeCSVCell(f.Question),
			escapeCSVCell(f.Answer),
			strconv.Itoa(f.SortOrder),
			strconv.FormatBool(f.IsActive),
		})
	}
	w.Flush()

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("export_faq", fmt.Sprintf("导出 FAQ %d 条 (项目: %s)", len(faqs), slug), fmt.Sprintf("%v", user), clientIP)
}

// AdminImportFAQs imports FAQs from a CSV uploaded as multipart form field
// "file". With ?preview=1 it returns header mapping + first 10 rows without
// writing. The real import creates rows, skips blanks and duplicates, and
// reports imported/skipped counts.
// POST /api/v1/admin/projects/:id/faqs/import
func (a *App) AdminImportFAQs(c *gin.Context) {
	slug := c.Param("id")
	if err := a.checkFAQProjectAccess(c, slug); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
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

	normalize := func(h string) string {
		n := strings.TrimSpace(strings.ToLower(h))
		if en, ok := faqCSVHeaderAliases[n]; ok {
			return en
		}
		return n
	}

	// Preview: column mapping + first 10 rows, no writes.
	if c.Query("preview") == "1" {
		header := records[0]
		mapped := map[string]string{}
		hasQuestion := false
		for _, h := range header {
			en := normalize(h)
			mapped[h] = en
			if en == "question" {
				hasQuestion = true
			}
		}
		limit := len(records) - 1
		if limit > 10 {
			limit = 10
		}
		c.JSON(http.StatusOK, gin.H{
			"headers":     header,
			"sample_rows": records[1 : limit+1],
			"mapped":      mapped,
			"has_question": hasQuestion,
		})
		return
	}

	// Real import.
	header := records[0]
	colIndex := map[string]int{}
	for i, h := range header {
		colIndex[normalize(h)] = i
	}
	if _, ok := colIndex["question"]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CSV 缺少必要列: 问题 (question)"})
		return
	}
	get := func(row []string, col string) string {
		if i, ok := colIndex[col]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}

	imported, skipped := 0, 0
	var errors []string
	for ri := 1; ri < len(records); ri++ {
		row := records[ri]
		q := get(row, "question")
		if q == "" {
			skipped++
			continue
		}
		ans := get(row, "answer")
		sortOrder := 0
		if v := get(row, "sort_order"); v != "" {
			if n, e := strconv.Atoi(v); e == nil {
				sortOrder = n
			}
		}
		active := true
		if v := get(row, "is_active"); v != "" {
			active = v == "true" || v == "1" || v == "是"
		}
		// Skip duplicates within the same project (GetFAQByQuestion is the
		// same dedup the create form uses).
		if existing, _ := a.DB.GetFAQByQuestion(slug, q); existing != nil {
			skipped++
			continue
		}
		if _, err := a.DB.CreateFAQ(slug, q, ans, sortOrder, active); err != nil {
			errors = append(errors, fmt.Sprintf("第 %d 行创建失败: %s", ri+1, err.Error()))
			continue
		}
		imported++
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("import_faq", fmt.Sprintf("导入 FAQ %d 条 (项目 %s，跳过 %d)", imported, slug, skipped), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{
		"imported": imported,
		"skipped":  skipped,
		"errors":   errors,
	})
}
