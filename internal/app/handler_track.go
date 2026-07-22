package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
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
	usefulVotes, _ := a.DB.CountVotesByType(fb.ID, "useful", "feedback")
	encVotes, _ := a.DB.CountVotesByType(fb.ID, "encountered", "feedback")
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
		"file_paths":        fb.FilePaths,
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

// PublicTrackLookup unifies the token and email portals behind a single input:
// an email address lists all feedback submitted under that inbox, while any
// other value is treated as a tracking token and returns the single item.
func (a *App) PublicTrackLookup(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入跟踪令牌或邮箱"})
		return
	}
	if emailRegex.MatchString(q) {
		list, err := a.DB.ListFeedbackByContactEmail(q, 50)
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
		c.JSON(http.StatusOK, gin.H{"type": "list", "items": items})
		return
	}

	// Otherwise treat the input as a tracking token.
	fb, err := a.DB.GetFeedbackByTrackingToken(q)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到对应的反馈记录"})
		return
	}
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
	usefulVotes, _ := a.DB.CountVotesByType(fb.ID, "useful", "feedback")
	encVotes, _ := a.DB.CountVotesByType(fb.ID, "encountered", "feedback")
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
		"type":              "feedback",
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
		"file_paths":        fb.FilePaths,
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
	// Preview and download share the exact same resolver, so a submitter can
	// never see more via the thumb endpoint than via the raw download.
	abs, status, msg := a.resolveTrackFile(c, noteID, idx)
	if status != http.StatusOK {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	c.File(abs)
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
	already, err := a.DB.InsertVote(id, voterKey, voteType, "feedback")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "投票失败"})
		return
	}
	votes, _ := a.DB.CountVotesByType(id, voteType, "feedback")
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
	total, _ := a.DB.CountPublicRoadmap(slug, category)
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

// AdminListRoadmap returns all roadmap entries (board-placed or public) for
// the admin roadmap management tab.
func (a *App) AdminListRoadmap(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	items, total, err := a.DB.ListRoadmapForAdmin(limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if items == nil {
		items = []database.RoadmapAdminItem{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
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

// AdminSetRoadmapMeta updates curation fields (order, target date, owner,
// release version) for a roadmap entry without changing its public/status.
func (a *App) AdminSetRoadmapMeta(c *gin.Context) {
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
		Order      int   `json:"order"`
		TargetDate int64 `json:"target_date"`
		Owner      string `json:"owner"`
		Release    string `json:"release"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if err := a.DB.SetRoadmapMeta(id, req.Order, req.TargetDate, req.Owner, req.Release); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	user, _ := c.Get("admin_user")
	a.DB.InsertAuditLog("set_roadmap_meta", fmt.Sprintf("反馈 #%d 策展: order=%d owner=%s release=%s", id, req.Order, req.Owner, req.Release), fmt.Sprintf("%v", user), middleware.GetClientIP(c))
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

// AdminBulkRoadmap applies a status/public change to many roadmap entries at
// once, reusing the per-feedback permission check and SetRoadmap logic.
func (a *App) AdminBulkRoadmap(c *gin.Context) {
	var req struct {
		IDs    []int64 `json:"ids"`
		Status string  `json:"status"`
		Public bool    `json:"public"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未选择任何条目"})
		return
	}
	if req.Status != "" {
		valid := map[string]bool{"planning": true, "in_progress": true, "released": true}
		if !valid[req.Status] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的看板状态"})
			return
		}
	}
	var failed []int64
	for _, id := range req.IDs {
		if _, deny := a.checkFeedbackWritePerm(c, id); deny != "" {
			failed = append(failed, id)
			continue
		}
		if err := a.DB.SetRoadmap(id, req.Public, req.Status); err != nil {
			failed = append(failed, id)
		}
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("bulk_roadmap", fmt.Sprintf("批量路线图: %d 条 status=%s public=%v", len(req.IDs), req.Status, req.Public), fmt.Sprintf("%v", user), clientIP)
	if len(failed) > 0 {
		c.JSON(http.StatusOK, gin.H{"message": "部分更新失败", "failed": failed})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已批量更新"})
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
	l := &anonVoteLimiter{
		maxVotes: maxVotes,
		window:   window,
		hits:     make(map[string][]time.Time),
	}
	// Periodically evict stale entries to prevent unbounded memory growth.
	// Without this, IPs that make a single request and never return leave
	// their entry in the map forever.
	go func() {
		ticker := time.NewTicker(window)
		for range ticker.C {
			l.mu.Lock()
			cutoff := time.Now().Add(-l.window)
			for ip, times := range l.hits {
				kept := times[:0]
				for _, t := range times {
					if t.After(cutoff) {
						kept = append(kept, t)
					}
				}
				if len(kept) == 0 {
					delete(l.hits, ip)
				} else {
					l.hits[ip] = kept
				}
			}
			l.mu.Unlock()
		}
	}()
	return l
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
