package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"feedshit/internal/database"
	"feedshit/internal/middleware"
)

// ========== Webhook Notification ==========

// SendWebhookNotification sends a new-feedback webhook notification.
func (a *App) SendWebhookNotification(fb *database.Feedback) {
	a.sendWebhookEvent("new_feedback", map[string]interface{}{
		"id":          fb.ID,
		"project_id":  fb.ProjectID,
		"title":       fb.Title,
		"description": fb.Description,
		"custom_data": fb.CustomData,
		"client_ip":   fb.ClientIP,
		"created_at":  fb.CreatedAt.Format(time.RFC3339),
	}, fb)
}

// sendWebhookEvent sends a webhook notification for any event type.
func (a *App) sendWebhookEvent(event string, data map[string]interface{}, fb *database.Feedback) {
	// Build the platform-specific payload the same way as before.
	webhookURL := a.DB.GetConfig("webhook_url")
	if webhookURL == "" {
		webhookURL = a.Cfg.WebhookURL
	}

	var payload []byte
	var err error
	var platform string

	if fb != nil {
		platform = a.DB.GetConfig("webhook_type")
		if platform == "" && webhookURL != "" {
			platform = detectWebhookPlatform(webhookURL)
		}
		switch platform {
		case "feishu":
			payload, err = buildFeishuCard(event, data, fb)
		case "dingtalk":
			payload, err = buildDingTalkCard(event, data, fb)
		case "slack":
			payload, err = buildSlackCard(event, data, fb)
		case "wecom":
			payload, err = buildWeComCard(event, data, fb)
		default:
			wrapper := map[string]interface{}{
				"event":     event,
				"data":      data,
				"timestamp": time.Now().Format(time.RFC3339),
			}
			payload, err = json.Marshal(wrapper)
		}
	} else {
		wrapper := map[string]interface{}{
			"event":     event,
			"data":      data,
			"timestamp": time.Now().Format(time.RFC3339),
		}
		payload, err = json.Marshal(wrapper)
	}
	if err != nil {
		log.Printf("[WEBHOOK] Failed to build %s payload for %s: %v", platform, event, err)
		return
	}

	slug := ""
	if fb != nil {
		slug = fb.ProjectID
	}

	// Enqueue for subscription-based delivery (with retry + signature at send time).
	// The legacy single webhook_url channel has been removed (app.go:1668); all
	// outbound webhooks now go through HMAC-signed subscription deliveries.
	if eerr := a.DB.EnqueueWebhook(event, string(payload), slug); eerr != nil {
		log.Printf("[WEBHOOK] Enqueue failed for %s: %v", event, eerr)
	}
}

// ProcessWebhookOutbox delivers due webhook outbox rows with HMAC signing and exponential backoff.
func (a *App) ProcessWebhookOutbox() {
	batch, err := a.DB.GetDueOutbox(time.Now().Unix(), 50)
	if err != nil {
		log.Printf("[WEBHOOK] outbox query failed: %v", err)
		return
	}
	for _, o := range batch {
		a.deliverWebhook(o)
	}
}

func (a *App) deliverWebhook(o database.WebhookOutbox) {
	const maxAttempts = 8
	nextAttempt := o.Attempts + 1

	req, err := http.NewRequest(http.MethodPost, o.URL, bytes.NewReader([]byte(o.Payload)))
	if err != nil {
		a.DB.MarkOutboxFailure(o.ID, err.Error(), nextAttempt, time.Now().Unix()+webhookBackoff(o.Attempts), maxAttempts)
		a.alertWebhookFailure(o.ID, o.URL, err.Error(), nextAttempt, maxAttempts)
		a.recordDelivery(o.SubscriptionID, "delivery", o.URL, o.Payload, 0, "", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if o.Secret != "" {
		mac := hmac.New(sha256.New, []byte(o.Secret))
		mac.Write([]byte(o.Payload))
		req.Header.Set("X-FeedShit-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		a.DB.MarkOutboxFailure(o.ID, err.Error(), nextAttempt, time.Now().Unix()+webhookBackoff(o.Attempts), maxAttempts)
		a.alertWebhookFailure(o.ID, o.URL, err.Error(), nextAttempt, maxAttempts)
		a.recordDelivery(o.SubscriptionID, "delivery", o.URL, o.Payload, 0, "", err.Error())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		a.DB.MarkOutboxSuccess(o.ID)
		log.Printf("[WEBHOOK] delivered outbox #%d to %s", o.ID, o.URL)
		a.recordDelivery(o.SubscriptionID, "delivery", o.URL, o.Payload, resp.StatusCode, string(body), "")
	} else {
		errMsg := fmt.Sprintf("status %d: %s", resp.StatusCode, string(body))
		a.DB.MarkOutboxFailure(o.ID, errMsg, nextAttempt, time.Now().Unix()+webhookBackoff(o.Attempts), maxAttempts)
		a.alertWebhookFailure(o.ID, o.URL, errMsg, nextAttempt, maxAttempts)
		a.recordDelivery(o.SubscriptionID, "delivery", o.URL, o.Payload, resp.StatusCode, string(body), errMsg)
	}
}

// recordDelivery writes a webhook delivery attempt to the history table.
func (a *App) recordDelivery(subID int64, event, url, requestBody string, status int, responseBody, errText string) {
	if err := a.DB.RecordWebhookDelivery(subID, event, url, requestBody, status, responseBody, errText, time.Now().Unix()); err != nil {
		log.Printf("[WEBHOOK] record delivery failed: %v", err)
	}
}

// AdminTestWebhook sends a sample "test" event to a webhook subscription
// (HMAC-signed, but NOT via the retry outbox) and returns the response.
func (a *App) AdminTestWebhook(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	subs, err := a.DB.ListWebhookSubscriptions()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询订阅失败"})
		return
	}
	var sub *database.WebhookSubscription
	for i := range subs {
		if subs[i].ID == id {
			sub = &subs[i]
			break
		}
	}
	if sub == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "订阅不存在"})
		return
	}
	if !sub.IsActive {
		c.JSON(http.StatusBadRequest, gin.H{"error": "订阅未启用"})
		return
	}

	payloadObj := map[string]interface{}{
		"event":          "test",
		"subscription_id": sub.ID,
		"sample":         true,
		"timestamp":      time.Now().Format(time.RFC3339),
	}
	payload, _ := json.Marshal(payloadObj)
	secret := sub.Secret

	req, err := http.NewRequest(http.MethodPost, sub.URL, bytes.NewReader(payload))
	if err != nil {
		a.recordDelivery(sub.ID, "test", sub.URL, string(payload), 0, "", err.Error())
		c.JSON(http.StatusOK, gin.H{"status": 0, "error": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(payload)
		req.Header.Set("X-FeedShit-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		a.recordDelivery(sub.ID, "test", sub.URL, string(payload), 0, "", err.Error())
		c.JSON(http.StatusOK, gin.H{"status": 0, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	a.recordDelivery(sub.ID, "test", sub.URL, string(payload), resp.StatusCode, string(body), "")
	c.JSON(http.StatusOK, gin.H{
		"status":         resp.StatusCode,
		"body":           string(body),
		"subscription_id": sub.ID,
	})
}

// AdminWebhookDeliveries returns recent delivery history for a subscription.
func (a *App) AdminWebhookDeliveries(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	deliveries, err := a.DB.ListWebhookDeliveries(id, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询投递历史失败"})
		return
	}
	total, success, failed, rate, serr := a.DB.WebhookDeliveryStats(id)
	if serr != nil {
		// Stats are non-critical; fall back to zeros rather than failing the whole request.
		total, success, failed, rate = 0, 0, 0, 0
	}
	c.JSON(http.StatusOK, gin.H{
		"deliveries": deliveries,
		"stats": gin.H{
			"total":        total,
			"success":      success,
			"failed":       failed,
			"success_rate": rate,
		},
	})
}

// alertWebhookFailure sends an alert email when a webhook outbox reaches max attempts.
func (a *App) alertWebhookFailure(id int64, url, lastErr string, attempts, maxAttempts int) {
	if attempts < maxAttempts {
		return // only alert on final failure
	}
	log.Printf("[WEBHOOK] ALERT: outbox #%d to %s failed after %d attempts: %s", id, url, attempts, lastErr)
	// Send alert email to the configured admin notice address
	to := a.DB.GetConfig("smtp_to")
	if to == "" {
		return
	}
	subject := fmt.Sprintf("[FeedShit] Webhook 投递失败: #%d", id)
	body := fmt.Sprintf(
		`<html><body><h2>Webhook 投递失败</h2>
<p>Webhook outbox <strong>#%d</strong> 在 %d 次重试后仍然失败。</p>
<table><tr><td>URL</td><td>%s</td></tr>
<tr><td>最后错误</td><td>%s</td></tr></table>
<p>请在后台检查 Webhook 配置是否正确。</p></body></html>`,
		id, maxAttempts, url, htmlEscaper(lastErr))
	go a.Mailer.Send(to, subject, body)
}

// htmlEscaper escapes special HTML characters (shorthand for the html package).
func htmlEscaper(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s, "&", "&amp;"), "<", "&lt;"), ">", "&gt;")
}

// webhookBackoff returns the retry delay (seconds) for a given attempt count, capped at 1h.
func webhookBackoff(attempts int) int64 {
	d := 30 * (1 << uint(attempts))
	if d > 3600 {
		d = 3600
	}
	return int64(d)
}

// ========== M6 Webhook Subscriptions (admin CRUD) ==========

func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}

func (a *App) AdminListWebhookSubscriptions(c *gin.Context) {
	subs, err := a.DB.ListWebhookSubscriptions()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if subs == nil {
		subs = []database.WebhookSubscription{}
	}
	for i := range subs {
		subs[i].Secret = maskSecret(subs[i].Secret)
	}
	c.JSON(http.StatusOK, gin.H{"subscriptions": subs})
}

func (a *App) AdminCreateWebhookSubscription(c *gin.Context) {
	var req struct {
		ProjectSlug string `json:"project_slug"`
		URL         string `json:"url"`
		Secret      string `json:"secret"`
		Events      string `json:"events"`
		IsActive    bool   `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "URL 不能为空"})
		return
	}
	if req.Events == "" {
		req.Events = "*"
	}
	id, err := a.DB.CreateWebhookSubscription(req.ProjectSlug, req.URL, req.Secret, req.Events, req.IsActive)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("create_webhook_sub", fmt.Sprintf("创建 Webhook 订阅 #%d", id), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusCreated, gin.H{"id": id, "message": "已创建"})
}

func (a *App) AdminUpdateWebhookSubscription(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	var req struct {
		URL      string `json:"url"`
		Secret   string `json:"secret"`
		Events   string `json:"events"`
		IsActive *bool  `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	// Preserve existing secret when not provided
	if req.Secret == "" {
		subs, _ := a.DB.ListWebhookSubscriptions()
		for _, s := range subs {
			if s.ID == id {
				req.Secret = s.Secret
				break
			}
		}
	}
	if err := a.DB.UpdateWebhookSubscription(id, req.URL, req.Secret, req.Events, req.IsActive); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_webhook_sub", fmt.Sprintf("更新 Webhook 订阅 #%d", id), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

func (a *App) AdminDeleteWebhookSubscription(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	if err := a.DB.DeleteWebhookSubscription(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_webhook_sub", fmt.Sprintf("删除 Webhook 订阅 #%d", id), fmt.Sprintf("%v", user), clientIP)
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}
