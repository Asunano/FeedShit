package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"feedshit/internal/database"
	"feedshit/internal/middleware"
)

// emailRegex validates submitter email addresses for the email portal lookup.
var emailRegex = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

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
	usefulVotes, _ := a.DB.CountVotesByType(fb.ID, "useful")
	encVotes, _ := a.DB.CountVotesByType(fb.ID, "encountered")
	history, _ := a.DB.ListStatusHistory(fb.ID)
	histItems := make([]gin.H, 0, len(history))
	for _, h := range history {
		histItems = append(histItems, gin.H{
			"from_status": h.FromStatus,
			"to_status":   h.ToStatus,
			"changed_by":  h.ChangedBy,
			"note":        h.Note,
			"created_at":  h.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	resp := gin.H{
		"id":                fb.ID,
		"project_id":        fb.ProjectID,
		"title":             fb.Title,
		"description":       fb.Description,
		"status":            fb.Status,
		"category":          fb.Category,
		"priority":          fb.Priority,
		"useful_votes":      usefulVotes,
		"encountered_votes": encVotes,
		"created_at":        fb.CreatedAt.Format("2006-01-02 15:04:05"),
		"allow_rating":      fb.Status == database.StatusResolved || fb.RatingOpen,
		"status_history":    histItems,
		"notes":             publicNotes,
	}
	if rating != nil {
		resp["rating"] = rating.Score
		resp["rating_comment"] = rating.Comment
	}

	c.JSON(http.StatusOK, resp)
}

// PublicListByEmail lets a submitter list all feedback they submitted under a
// given email address (the email portal on the tracking page). Each item
// returns its tracking token so the submitter can open the corresponding
// tracking page; tokens are unguessable, so this does not leak data to
// third parties who do not control that inbox.
func (a *App) PublicListByEmail(c *gin.Context) {
	email := strings.TrimSpace(c.Query("email"))
	if email == "" || !emailRegex.MatchString(email) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供有效的邮箱地址"})
		return
	}
	list, err := a.DB.ListFeedbackByContactEmail(email, 50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if list == nil {
		list = []database.Feedback{}
	}
	items := make([]gin.H, 0, len(list))
	for _, f := range list {
		items = append(items, gin.H{
			"id":         f.ID,
			"project_id": f.ProjectID,
			"title":      f.Title,
			"status":     f.Status,
			"updated_at": time.Unix(f.UpdatedAt, 0).Format("2006-01-02 15:04:05"),
			"token":      f.TrackingToken,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
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

	// Cap replies per tracking token so a leaked token + proxy pool cannot
	// flood a single feedback with follow-ups.
	if !a.ReplyLimiter.allow(token) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "回复过于频繁，请稍后再试"})
		return
	}

	// Accept optional file attachments (multipart). Text-only replies remain
	// supported; a reply with neither content nor a file is rejected.
	filePaths, err := a.saveUploadFiles(c, fb.ProjectID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	content := strings.TrimSpace(c.PostForm("content"))
	if len(content) > 2000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "回复内容最多 2000 字"})
		return
	}
	if content == "" && filePaths == "[]" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "回复内容或附件至少填写一项"})
		return
	}

	noteID, err := a.DB.InsertSubmitterReply(fb.ID, content, filePaths)
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

// PublicServeTrackFile serves a file attached to a feedback note, accessible
// only to the submitter who owns that feedback — and only for public notes, so
// internal admin attachments never leak. The file path is derived entirely
// from the database (never from client input) and is validated against the
// uploads/ subtree to block traversal.
func (a *App) PublicServeTrackFile(c *gin.Context) {
	token := strings.TrimSpace(c.Query("token"))
	noteID, err := strconv.ParseInt(c.Query("note"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少必要的回复参数"})
		return
	}
	idx := 0
	if v := c.Query("i"); v != "" {
		idx, err = strconv.Atoi(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的附件索引"})
			return
		}
	}
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少跟踪令牌"})
		return
	}

	fb, err := a.DB.GetFeedbackByTrackingToken(token)
	if err != nil || fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到对应的反馈记录"})
		return
	}
	note, err := a.DB.GetFeedbackNote(noteID)
	if err != nil || note == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到对应的回复"})
		return
	}
	if note.FeedbackID != fb.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该附件"})
		return
	}
	if !note.IsPublic {
		// Internal admin notes are never exposed to submitters.
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该附件"})
		return
	}

	var paths []string
	if err := json.Unmarshal([]byte(note.FilePaths), &paths); err != nil || len(paths) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "该回复没有附件"})
		return
	}
	if idx < 0 || idx >= len(paths) {
		c.JSON(http.StatusNotFound, gin.H{"error": "附件不存在"})
		return
	}
	relPath := paths[idx]

	cleaned := filepath.Clean(filepath.FromSlash(relPath))
	if strings.Contains(cleaned, "..") {
		c.JSON(http.StatusForbidden, gin.H{"error": "非法路径"})
		return
	}

	absDataDirResolved, err := filepath.EvalSymlinks(a.Cfg.DataDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "路径解析失败"})
		return
	}
	absBaseResolved := filepath.Join(absDataDirResolved, "uploads")
	absPath := filepath.Join(absDataDirResolved, cleaned)
	absResolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
		return
	}
	if !strings.HasPrefix(absResolved, absBaseResolved+string(os.PathSeparator)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "非法路径"})
		return
	}
	info, err := os.Stat(absResolved)
	if err != nil || info.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
		return
	}

	c.File(absResolved)
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
	if fb.Status != database.StatusResolved && !fb.RatingOpen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前不可评分"})
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

	voteType := strings.TrimSpace(c.Query("type"))
	if voteType == "" {
		voteType = "useful"
	}
	if voteType != "useful" && voteType != "encountered" {
		voteType = "useful"
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
		// Cap anonymous votes per client IP to blunt vote-farming via
		// proxy pools / User-Agent rotation.
		if !a.AnonVoteLimiter.allow(middleware.GetClientIP(c)) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "投票过于频繁，请稍后再试"})
			return
		}
		ua := c.GetHeader("User-Agent")
		h := sha256.Sum256([]byte(middleware.GetClientIP(c) + "|" + ua))
		voterKey = "anon:" + hex.EncodeToString(h[:])
	}
	already, err := a.DB.InsertVote(id, voterKey, voteType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "投票失败"})
		return
	}
	votes, _ := a.DB.CountVotesByType(id, voteType)
	c.JSON(http.StatusOK, gin.H{"type": voteType, "voted": !already, "votes": votes})
}

// PublicNeedHelp lets a submitter revert a resolved/closed feedback to "processing"
// when the resolution did not actually solve their problem (track page #6).
func (a *App) PublicNeedHelp(c *gin.Context) {
	token := strings.TrimSpace(c.Param("token"))
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
	if fb.Status != database.StatusResolved && fb.Status != database.StatusClosed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅已解决或已关闭的反馈可标记为仍需帮助"})
		return
	}

	if err := a.DB.UpdateFeedbackStatus(fb.ID, database.StatusProcessing, fb.Tags); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	if err := a.DB.RecordStatusChange(fb.ID, fb.Status, database.StatusProcessing, "提交者", "仍需帮助"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "记录状态变更失败"})
		return
	}
	user := "提交者"
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("need_help", fmt.Sprintf("反馈 #%d 提交者标记仍需帮助", fb.ID), user, clientIP)
	go a.sendWebhookEvent("status_change", map[string]interface{}{
		"id":         fb.ID,
		"project_id": fb.ProjectID,
		"title":      fb.Title,
		"status":     database.StatusProcessing,
		"operator":   user,
	}, fb)

	c.JSON(http.StatusOK, gin.H{"message": "已标记为仍需帮助，状态回退为处理中"})
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

// anonVoteLimiter caps anonymous votes per client IP over a sliding window to
// blunt vote-farming (the dedup key includes the User-Agent, which an attacker
// can rotate at will). Legitimate users rarely approach the ceiling.
type anonVoteLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	maxVotes int
	hits     map[string][]time.Time
}

func newAnonVoteLimiter(maxVotes int, window time.Duration) *anonVoteLimiter {
	return &anonVoteLimiter{
		maxVotes: maxVotes,
		window:   window,
		hits:     make(map[string][]time.Time),
	}
}

// allow records one vote attempt for ip and reports whether it is within the
// cap. Unattributable IPs (empty) are never blocked.
func (l *anonVoteLimiter) allow(ip string) bool {
	if ip == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)
	kept := l.hits[ip][:0]
	for _, t := range l.hits[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.maxVotes {
		l.hits[ip] = kept
		return false
	}
	kept = append(kept, now)
	l.hits[ip] = kept
	return true
}
