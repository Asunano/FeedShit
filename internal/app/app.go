package app

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"feedshit/internal/config"
	"feedshit/internal/database"
	"feedshit/internal/email"
	"feedshit/internal/middleware"
)

// App holds all shared dependencies for HTTP handlers.
type App struct {
	Cfg          *config.Config
	DB           *database.Database
	SM           *middleware.SessionManager
	RL           *middleware.RateLimiter
	Mailer       *email.Mailer
	NonceCache   *middleware.NonceCache
	LoginTracker *middleware.LoginAttemptTracker
	// AnonVoteLimiter caps anonymous votes per IP to stop vote-farming.
	AnonVoteLimiter *anonVoteLimiter
	// ReplyLimiter caps submitter replies per tracking token to blunt
	// reply-spam (the global IP rate limit is bypassable via proxy pools).
	ReplyLimiter *anonVoteLimiter

	// M7: per-token rate limiting (in-memory, single-instance)
	tokenMu       sync.Mutex
	tokenHourHits map[string]int
}

// New creates a new App instance.
func New(cfg *config.Config, db *database.Database, sm *middleware.SessionManager, rl *middleware.RateLimiter, mailer *email.Mailer) *App {
	a := &App{
		Cfg:           cfg,
		DB:            db,
		SM:            sm,
		RL:            rl,
		Mailer:        mailer,
		NonceCache:    middleware.NewNonceCache(),
		LoginTracker:  middleware.NewLoginAttemptTracker(10),
		AnonVoteLimiter: newAnonVoteLimiter(100, 24*time.Hour),
		ReplyLimiter:    newAnonVoteLimiter(30, 24*time.Hour),
		tokenHourHits: make(map[string]int),
	}
	// Periodically clear per-token hourly hit counters to avoid unbounded memory
	// growth. Keys embed the hour string, so clearing hourly is safe — counters
	// reset each hour anyway. Fixes the in-memory leak in APITokenAuthMiddleware.
	go func() {
		ticker := time.NewTicker(time.Hour)
		for range ticker.C {
			a.tokenMu.Lock()
			a.tokenHourHits = make(map[string]int)
			a.tokenMu.Unlock()
		}
	}()
	// Load CDN config from DB at startup
	if cdn := db.GetConfig("cdn_provider"); cdn != "" {
		middleware.SetCDNProvider(cdn)
	}
	if tp := db.GetConfig("trusted_proxies"); tp != "" {
		var proxies []string
		for _, p := range strings.Split(tp, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				proxies = append(proxies, p)
			}
		}
		middleware.SetTrustedProxies(proxies)
	}
	return a
}

// ========== Feedback Notes (Replies / Internal Notes) ==========

func (a *App) AdminAddFeedbackNote(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	// Verify feedback exists
	fb, err := a.DB.GetFeedback(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}

	// Check write permission
	if _, deny := a.checkFeedbackWritePerm(c, id); deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}

	ct := c.ContentType()
	var content, filePaths string
	var isPublic bool
	if strings.HasPrefix(ct, "multipart/form-data") {
		var err error
		filePaths, err = a.saveUploadFiles(c, fb.ProjectID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		content = strings.TrimSpace(c.PostForm("content"))
		v := c.PostForm("is_public")
		isPublic = v == "true" || v == "on" || v == "1"
	} else {
		var req struct {
			Content  string `json:"content"`
			IsPublic bool   `json:"is_public"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
			return
		}
		content = strings.TrimSpace(req.Content)
		isPublic = req.IsPublic
	}
	if content == "" && filePaths == "[]" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "内容或附件至少填写一项"})
		return
	}

	user, _ := c.Get("admin_user")
	author := fmt.Sprintf("%v", user)

	noteID, err := a.DB.InsertFeedbackNote(id, content, author, isPublic, filePaths)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}

	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("add_note", fmt.Sprintf("反馈 #%d 添加备注", id), author, clientIP)

	// Notify submitter when a public reply is added
	if isPublic && fb.ContactEmail != "" {
		vars := map[string]string{
			"id":           fmt.Sprintf("%d", fb.ID),
			"title":        fb.Title,
			"note_content": content,
			"author":       author,
		}
		subject := email.BuildReplySubject(a.DB, vars)
		body := email.BuildReplyBody(a.DB, vars)
		go a.Mailer.SendStatusChangeNotification(fb, subject, body)
	}

	// Webhook notification for new note
	go a.sendWebhookEvent("new_note", map[string]interface{}{
		"id":         fb.ID,
		"project_id": fb.ProjectID,
		"title":      fb.Title,
		"note":       content,
		"is_public":  isPublic,
		"author":     author,
	}, fb)

	c.JSON(http.StatusCreated, gin.H{"message": "备注已添加", "id": noteID})
}

func (a *App) AdminListFeedbackNotes(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	// Authorization: only users with read access to the feedback may list its notes.
	fb, deny := a.checkFeedbackReadPerm(c, id)
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}
	if deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}

	notes, err := a.DB.ListFeedbackNotes(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if notes == nil {
		notes = []database.FeedbackNote{}
	}
	c.JSON(http.StatusOK, gin.H{"notes": notes})
}

func (a *App) AdminDeleteFeedbackNote(c *gin.Context) {
	noteID, err := strconv.ParseInt(c.Param("noteId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	// Resolve the note to find its parent feedback, then enforce write permission.
	note, err := a.DB.GetFeedbackNote(noteID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if note == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "备注不存在"})
		return
	}

	fb, deny := a.checkFeedbackWritePerm(c, note.FeedbackID)
	if fb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "反馈不存在"})
		return
	}
	if deny != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": deny})
		return
	}

	if err := a.DB.DeleteFeedbackNote(noteID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_note", fmt.Sprintf("删除备注 #%d", noteID), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "备注已删除"})
}
