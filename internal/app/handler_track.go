package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"feedshit/internal/database"
	"feedshit/internal/middleware"
)

// ========== Public Tracking (Submitter Self-Service) ==========

// PublicTrackFeedback returns feedback details by tracking token.
// Only returns public-safe fields (no IP, no internal notes).
func (a *App) PublicTrackFeedback(c *gin.Context) {
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少跟踪令牌"})
		return
	}

	fb, err := a.DB.GetFeedbackByTrackingToken(token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到对应的反馈记录"})
		return
	}

	// Get public notes only
	notes, err := a.DB.ListFeedbackNotes(fb.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询备注失败"})
		return
	}
	var publicNotes []database.FeedbackNote
	for _, n := range notes {
		if n.IsPublic {
			publicNotes = append(publicNotes, n)
		}
	}
	if publicNotes == nil {
		publicNotes = []database.FeedbackNote{}
	}

	rating, _ := a.DB.GetRating(fb.ID)
	resp := gin.H{
		"id":           fb.ID,
		"project_id":   fb.ProjectID,
		"title":        fb.Title,
		"description":  fb.Description,
		"status":       fb.Status,
		"category":     fb.Category,
		"priority":     fb.Priority,
		"votes":        fb.Votes,
		"created_at":   fb.CreatedAt.Format("2006-01-02 15:04:05"),
		"allow_rating": fb.Status == database.StatusResolved,
		"notes":        publicNotes,
	}
	if rating != nil {
		resp["rating"] = rating.Score
		resp["rating_comment"] = rating.Comment
	}

	c.JSON(http.StatusOK, resp)
}

// PublicSubmitReply allows a submitter to add a follow-up reply to their feedback.
func (a *App) PublicSubmitReply(c *gin.Context) {
	token := strings.TrimSpace(c.PostForm("token"))
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少跟踪令牌"})
		return
	}

	fb, err := a.DB.GetFeedbackByTrackingToken(token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到对应的反馈记录"})
		return
	}

	content := strings.TrimSpace(c.PostForm("content"))
	if content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "回复内容不能为空"})
		return
	}
	if len(content) > 2000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "回复内容最多 2000 字"})
		return
	}

	noteID, err := a.DB.InsertSubmitterReply(fb.ID, content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存回复失败"})
		return
	}

	// F3: Notify admin about submitter reply
	go a.Mailer.SendSubmitterReplyNotification(fb, content)

	// F5: Webhook event for submitter reply
	go a.sendWebhookEvent("new_note", map[string]interface{}{
		"id":         fb.ID,
		"project_id": fb.ProjectID,
		"title":      fb.Title,
		"note":       content,
		"is_public":  true,
		"author":     "提交者",
	}, fb)

	c.JSON(http.StatusOK, gin.H{
		"message": "回复已提交",
		"note_id": noteID,
	})
}

// PublicSubmitRating lets a submitter rate a resolved feedback via their tracking token (M2 CSAT).
func (a *App) PublicSubmitRating(c *gin.Context) {
	token := strings.TrimSpace(c.Param("token"))
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少跟踪令牌"})
		return
	}
	fb, err := a.DB.GetFeedbackByTrackingToken(token)
	if err != nil || fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到对应的反馈记录"})
		return
	}
	if fb.Status != database.StatusResolved {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅已解决的反馈可评分"})
		return
	}

	var req struct {
		Score   int    `json:"score"`
		Comment string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.Score < 1 || req.Score > 5 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "评分必须为 1-5"})
		return
	}

	if err := a.DB.UpsertRating(fb.ID, req.Score, strings.TrimSpace(req.Comment)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存评分失败"})
		return
	}

	user := "提交者"
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("csat_rating", fmt.Sprintf("反馈 #%d 评分 %d", fb.ID, req.Score), user, clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "评分已提交", "score": req.Score})
}

// PublicVoteFeedback records an upvote on a feedback from any visitor (M4).
func (a *App) PublicVoteFeedback(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	// Verify feedback exists and project is active / not archived
	fb, err := a.DB.GetFeedback(id)
	if err != nil || fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}
	proj, projErr := a.DB.GetProjectBySlug(fb.ProjectID)
	if projErr != nil || proj == nil || !proj.IsActive || proj.IsArchived {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该项目已停用或已归档，无法投票"})
		return
	}
	var voterKey string
	if t := strings.TrimSpace(c.Query("token")); t != "" {
		voterKey = "tok:" + t
	} else {
		ua := c.GetHeader("User-Agent")
		h := sha256.Sum256([]byte(middleware.GetClientIP(c) + "|" + ua))
		voterKey = "anon:" + hex.EncodeToString(h[:])
	}
	already, err := a.DB.InsertVote(id, voterKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "投票失败"})
		return
	}
	votes, _ := a.DB.CountVotes(id)
	c.JSON(http.StatusOK, gin.H{"voted": !already, "votes": votes})
}

// PublicRoadmap returns public roadmap items for a project (M3).
func (a *App) PublicRoadmap(c *gin.Context) {
	slug := strings.TrimSpace(c.Query("slug"))
	category := strings.TrimSpace(c.Query("category"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	items, err := a.DB.GetPublicRoadmap(slug, category, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if items == nil {
		items = []database.RoadmapItem{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// AdminSetRoadmap toggles public visibility and/or board status of a feedback (M3).
func (a *App) AdminSetRoadmap(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	if _, deny := a.checkFeedbackWritePerm(c, id); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}
	var req struct {
		Public bool   `json:"public"`
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.Status != "" {
		valid := map[string]bool{"planning": true, "in_progress": true, "released": true}
		if !valid[req.Status] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的看板状态"})
			return
		}
	}
	if err := a.DB.SetRoadmap(id, req.Public, req.Status); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("set_roadmap", fmt.Sprintf("反馈 #%d 看板: public=%v status=%s", id, req.Public, req.Status), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}
