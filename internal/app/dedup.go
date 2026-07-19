package app

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"feedshit/internal/database"
)

// dupCandidate is the minimal projection returned by duplicate-detection endpoints.
// It exposes only id/title/summary/token — never project_id, content_hash or is_duplicate.
type dupCandidate struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Token   string `json:"token"`
}

// dupMaxSubmit reads the public submit-page candidate cap (DUP_MAX_SUBMIT_RESULTS).
// Default 5, clamped to the inclusive range [1, 50].
func dupMaxSubmit() int { return clampDup("DUP_MAX_SUBMIT_RESULTS", 5) }

// dupMaxAdmin reads the admin candidate cap (DUP_MAX_ADMIN_RESULTS).
// Default 10, clamped to the inclusive range [1, 50].
func dupMaxAdmin() int { return clampDup("DUP_MAX_ADMIN_RESULTS", 10) }

// clampDup parses an env int with a default and [1,50] clamp.
func clampDup(env string, def int) int {
	n, err := strconv.Atoi(os.Getenv(env))
	if err != nil || n < 1 {
		n = def
	}
	if n > 50 {
		n = 50
	}
	return n
}

// summarize truncates a description to ~120 chars and flattens newlines for display.
func summarize(desc string) string {
	desc = strings.ReplaceAll(desc, "\n", " ")
	desc = strings.ReplaceAll(desc, "\r", " ")
	desc = strings.Join(strings.Fields(desc), " ")
	runes := []rune(desc)
	if len(runes) > 120 {
		desc = string(runes[:120]) + "…"
	}
	return desc
}

// toCandidates maps feedbacks to the minimal dupCandidate projection.
func toCandidates(feedbacks []database.Feedback) []dupCandidate {
	cands := make([]dupCandidate, 0, len(feedbacks))
	for _, f := range feedbacks {
		cands = append(cands, dupCandidate{
			ID:      f.ID,
			Title:   f.Title,
			Summary: summarize(f.Description),
			Token:   f.TrackingToken,
		})
	}
	return cands
}

// PublicCheckDuplicate detects duplicate feedback for anonymous submitters.
// GET /api/v1/feedback/check-duplicate?q=<title+description>&project=<slug>
// Empty q/project yields {candidates:[]} with no error and no cross-project leak.
func (a *App) PublicCheckDuplicate(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	project := strings.TrimSpace(c.Query("project"))
	if q == "" || project == "" {
		c.JSON(http.StatusOK, gin.H{"candidates": []dupCandidate{}})
		return
	}
	// Guard pathological input: cap keyword length at 500 chars.
	runes := []rune(q)
	if len(runes) > 500 {
		q = string(runes[:500])
	}
	// Consistency contract: frontend sends q = title + ' ' + description; this
	// mirrors ComputeContentHash(title, description) via the same normalizeContent.
	hash := database.ComputeContentHashFromText(q)
	feedbacks, err := a.DB.FindExactDuplicates(project, hash, 0, dupMaxSubmit())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "检测失败"})
		return
	}
	if feedbacks == nil {
		feedbacks = []database.Feedback{}
	}
	c.JSON(http.StatusOK, gin.H{"candidates": toCandidates(feedbacks)})
}

// AdminSimilarFeedbacks lists same-project open feedback sharing the content hash.
// GET /api/v1/admin/feedbacks/:id/similar (editor+ write permission)
func (a *App) AdminSimilarFeedbacks(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	// Write permission check (returns a denial message when not allowed).
	if _, deny := a.checkFeedbackWritePerm(c, id); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}
	fb, err := a.DB.GetFeedback(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}
	if fb.ContentHash == "" {
		c.JSON(http.StatusOK, gin.H{"candidates": []dupCandidate{}})
		return
	}
	feedbacks, err := a.DB.FindExactDuplicates(fb.ProjectID, fb.ContentHash, fb.ID, dupMaxAdmin())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if feedbacks == nil {
		feedbacks = []database.Feedback{}
	}
	c.JSON(http.StatusOK, gin.H{"candidates": toCandidates(feedbacks)})
}
