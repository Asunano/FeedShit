package email

import (
	"crypto/tls"
	"fmt"
	"html"
	"log"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"feedshit/internal/database"
)

// Mailer handles async email notifications.
type Mailer struct {
	db      *database.Database
	baseURL string
}

// NewMailer creates a new Mailer instance.
func NewMailer(db *database.Database, baseURL string) *Mailer {
	return &Mailer{db: db, baseURL: baseURL}
}

// getEmailSMTPConfig reads email-related config keys individually (NOT a full table scan).
// This avoids scanning the entire config table on every email send.
func getEmailSMTPConfig(db *database.Database) map[string]string {
	keys := []string{"smtp_host", "smtp_port", "smtp_user", "smtp_pass", "smtp_from", "smtp_to", "notify_enable"}
	m := make(map[string]string, len(keys))
	for _, k := range keys {
		m[k] = db.GetConfig(k)
	}
	return m
}

// sanitizeHeader strips CR and LF characters from a value destined for an SMTP
// header field, preventing header injection (CRLF injection) attacks.
func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

// SendFeedbackNotification sends an email notification for a new feedback.
func (m *Mailer) SendFeedbackNotification(fb *database.Feedback) {
	cfg := getEmailSMTPConfig(m.db)

	enabled := cfg["notify_enable"]
	if enabled != "true" {
		return
	}

	host := cfg["smtp_host"]
	port := cfg["smtp_port"]
	user := cfg["smtp_user"]
	pass := cfg["smtp_pass"]
	from := cfg["smtp_from"]
	to := cfg["smtp_to"]

	if host == "" || to == "" {
		log.Printf("[MAIL] SMTP not configured, skipping notification for feedback #%d", fb.ID)
		return
	}

	if port == "" {
		port = "587"
	}
	if from == "" {
		from = user
	}

	portNum, err := strconv.Atoi(port)
	if err != nil {
		portNum = 587
	}

	subject := fmt.Sprintf("[FeedShit] 新反馈: %s (%s)", fb.Title, fb.ProjectID)
	adminLink := fmt.Sprintf("%s/admin/#feedback/%d", m.baseURL, fb.ID)

	// Escape user-controlled values to prevent XSS in email clients
	safeProject := html.EscapeString(fb.ProjectID)
	safeTitle := html.EscapeString(fb.Title)
	safeDesc := html.EscapeString(fb.Description)
	safeDesc = strings.ReplaceAll(safeDesc, "\n", "<br>")
	safeIP := html.EscapeString(fb.ClientIP)

	body := fmt.Sprintf(`
<html>
<body style="font-family: -apple-system, sans-serif; color: #333; max-width: 600px; margin: 0 auto;">
  <h2 style="color: #e53e3e;">📋 新反馈通知</h2>
  <table style="width: 100%%; border-collapse: collapse; margin: 16px 0;">
    <tr><td style="padding: 8px; border-bottom: 1px solid #eee; font-weight: bold; width: 100px;">项目</td><td style="padding: 8px; border-bottom: 1px solid #eee;">%s</td></tr>
    <tr><td style="padding: 8px; border-bottom: 1px solid #eee; font-weight: bold;">标题</td><td style="padding: 8px; border-bottom: 1px solid #eee;">%s</td></tr>
    <tr><td style="padding: 8px; border-bottom: 1px solid #eee; font-weight: bold;">描述</td><td style="padding: 8px; border-bottom: 1px solid #eee;">%s</td></tr>
    <tr><td style="padding: 8px; border-bottom: 1px solid #eee; font-weight: bold;">来源 IP</td><td style="padding: 8px; border-bottom: 1px solid #eee;">%s</td></tr>
    <tr><td style="padding: 8px; font-weight: bold;">提交时间</td><td style="padding: 8px;">%s</td></tr>
  </table>
  <p><a href="%s" style="display: inline-block; padding: 10px 20px; background: #e53e3e; color: white; text-decoration: none; border-radius: 4px;">在后台查看</a></p>
  <p style="color: #999; font-size: 12px; margin-top: 24px;">此邮件由 FeedShit 自动发送</p>
</body>
</html>`,
		safeProject,
		safeTitle,
		safeDesc,
		safeIP,
		fb.CreatedAt.Format("2006-01-02 15:04:05"),
		adminLink,
	)

	// Use custom template if configured, otherwise use default
	vars := map[string]string{
		"project":     fb.ProjectID,
		"title":       fb.Title,
		"description": fb.Description,
		"client_ip":   fb.ClientIP,
		"admin_url":   adminLink,
		"created_at":  fb.CreatedAt.Format("2006-01-02 15:04:05"),
		"id":          fmt.Sprintf("%d", fb.ID),
	}
	subject = BuildNotificationSubject(m.db, vars)
	body = BuildNotificationBody(m.db, vars)

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		from, to, sanitizeHeader(subject), body)

	addr := fmt.Sprintf("%s:%d", host, portNum)

	recipients := strings.Split(to, ",")
	for i := range recipients {
		recipients[i] = strings.TrimSpace(recipients[i])
	}

	auth := smtp.PlainAuth("", user, pass, host)
	if err := smtpSend(addr, auth, from, recipients, []byte(msg)); err != nil {
		log.Printf("[MAIL] Failed to send notification for feedback #%d: %v", fb.ID, err)
	} else {
		log.Printf("[MAIL] Notification sent for feedback #%d to %s", fb.ID, to)
	}
}

// SendSubmitterConfirmation sends a confirmation email with the self-service
// tracking link to the feedback submitter who opted in to notifications.
// It is a no-op when the submitter left no email, notifications are disabled,
// or SMTP is not configured.
func (m *Mailer) SendSubmitterConfirmation(fb *database.Feedback, trackURL string) {
	if fb.ContactEmail == "" {
		return
	}
	cfg := getEmailSMTPConfig(m.db)
	if cfg["notify_enable"] != "true" {
		return
	}
	host := cfg["smtp_host"]
	port := cfg["smtp_port"]
	user := cfg["smtp_user"]
	pass := cfg["smtp_pass"]
	from := cfg["smtp_from"]
	if host == "" {
		log.Printf("[MAIL] SMTP not configured, skipping submitter confirmation for feedback #%d", fb.ID)
		return
	}
	if port == "" {
		port = "587"
	}
	if from == "" {
		from = user
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		portNum = 587
	}

	vars := map[string]string{
		"id":        fmt.Sprintf("%d", fb.ID),
		"title":     fb.Title,
		"track_url": trackURL,
	}
	subject := BuildConfirmationSubject(m.db, vars)
	body := BuildConfirmationBody(m.db, vars)

	safeContact := sanitizeHeader(fb.ContactEmail)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		from, safeContact, sanitizeHeader(subject), body)

	addr := fmt.Sprintf("%s:%d", host, portNum)
	auth := smtp.PlainAuth("", user, pass, host)
	if err := smtpSend(addr, auth, from, []string{safeContact}, []byte(msg)); err != nil {
		log.Printf("[MAIL] Failed to send submitter confirmation for feedback #%d: %v", fb.ID, err)
	} else {
		log.Printf("[MAIL] Submitter confirmation sent for feedback #%d to %s", fb.ID, fb.ContactEmail)
	}
}

// BuildConfirmationSubject builds the subject for the submitter confirmation email.
func BuildConfirmationSubject(db *database.Database, vars map[string]string) string {
	tpl := db.GetConfig("email_template_subject")
	if tpl != "" {
		return renderTemplate(tpl, vars)
	}
	return fmt.Sprintf("[FeedShit] 我们已收到您的反馈 #%s", vars["id"])
}

// BuildConfirmationBody builds the HTML body for the submitter confirmation email,
// including the self-service tracking link promised by the opt-in block.
func BuildConfirmationBody(db *database.Database, vars map[string]string) string {
	tpl := db.GetConfig("email_template_body")
	if tpl != "" {
		return renderTemplate(tpl, vars)
	}
	safeTitle := html.EscapeString(vars["title"])
	trackURL := vars["track_url"]
	return fmt.Sprintf(`<html><body style="font-family:-apple-system,sans-serif;color:#333;max-width:600px;margin:0 auto">
<h2>我们已收到您的反馈</h2>
<p><strong>编号：</strong>#%s</p>
<p><strong>标题：</strong>%s</p>
<p>感谢您的反馈，我们会尽快处理。您可以通过以下链接随时查看处理进度和回复：</p>
<p><a href="%s" style="display:inline-block;padding:10px 20px;background:#e53e3e;color:white;text-decoration:none;border-radius:4px">查看反馈进度</a></p>
<p style="color:#999;font-size:12px;margin-top:24px">此邮件由 FeedShit 自动发送</p>
</body></html>`, vars["id"], safeTitle, trackURL)
}

// renderTemplate applies placeholder substitution to a template string.
// User-controlled values are HTML-escaped to prevent injection; URL fields are excluded.
func renderTemplate(tpl string, vars map[string]string) string {
	// URL fields are trusted (generated by the app, not user input)
	urlKeys := map[string]bool{"admin_url": true, "track_url": true}
	for k, v := range vars {
		if !urlKeys[k] {
			v = html.EscapeString(v)
			// Preserve newlines as <br> for text fields rendered in HTML
			if k == "description" || k == "note_content" {
				v = strings.ReplaceAll(v, "\n", "<br>")
			}
		}
		tpl = strings.ReplaceAll(tpl, "{{"+k+"}}", v)
	}
	return tpl
}

// BuildNotificationSubject builds the email subject using custom template or default.
func BuildNotificationSubject(db *database.Database, vars map[string]string) string {
	tpl := db.GetConfig("email_template_subject")
	if tpl != "" {
		return renderTemplate(tpl, vars)
	}
	return fmt.Sprintf("[FeedShit] 新反馈: %s (%s)", vars["title"], vars["project"])
}

// BuildNotificationBody builds the email HTML body using custom template or default.
func BuildNotificationBody(db *database.Database, vars map[string]string) string {
	tpl := db.GetConfig("email_template_body")
	if tpl != "" {
		return renderTemplate(tpl, vars)
	}
	// Default HTML template
	safeProject := html.EscapeString(vars["project"])
	safeTitle := html.EscapeString(vars["title"])
	safeDesc := html.EscapeString(vars["description"])
	safeDesc = strings.ReplaceAll(safeDesc, "\n", "<br>")
	safeIP := html.EscapeString(vars["client_ip"])
	adminURL := vars["admin_url"]
	createdAt := vars["created_at"]

	return fmt.Sprintf(`
<html>
<body style="font-family: -apple-system, sans-serif; color: #333; max-width: 600px; margin: 0 auto;">
  <h2 style="color: #e53e3e;">📋 新反馈通知</h2>
  <table style="width: 100%%; border-collapse: collapse; margin: 16px 0;">
    <tr><td style="padding: 8px; border-bottom: 1px solid #eee; font-weight: bold; width: 100px;">项目</td><td style="padding: 8px; border-bottom: 1px solid #eee;">%s</td></tr>
    <tr><td style="padding: 8px; border-bottom: 1px solid #eee; font-weight: bold;">标题</td><td style="padding: 8px; border-bottom: 1px solid #eee;">%s</td></tr>
    <tr><td style="padding: 8px; border-bottom: 1px solid #eee; font-weight: bold;">描述</td><td style="padding: 8px; border-bottom: 1px solid #eee;">%s</td></tr>
    <tr><td style="padding: 8px; border-bottom: 1px solid #eee; font-weight: bold;">来源 IP</td><td style="padding: 8px; border-bottom: 1px solid #eee;">%s</td></tr>
    <tr><td style="padding: 8px; border-bottom: 1px solid #eee; font-weight: bold;">提交时间</td><td style="padding: 8px; border-bottom: 1px solid #eee;">%s</td></tr>
  </table>
  <p><a href="%s" style="display: inline-block; padding: 10px 20px; background: #e53e3e; color: white; text-decoration: none; border-radius: 4px;">在后台查看</a></p>
  <p style="color: #999; font-size: 12px; margin-top: 24px;">此邮件由 FeedShit 自动发送</p>
</body>
</html>`, safeProject, safeTitle, safeDesc, safeIP, createdAt, adminURL)
}

// BuildStatusChangeSubject builds subject for status change notifications.
func BuildStatusChangeSubject(db *database.Database, vars map[string]string) string {
	tpl := db.GetConfig("email_template_subject")
	if tpl != "" {
		return renderTemplate(tpl, vars)
	}
	return fmt.Sprintf("[FeedShit] 反馈 #%s 状态更新", vars["id"])
}

// BuildStatusChangeBody builds HTML body for status change notifications.
func BuildStatusChangeBody(db *database.Database, vars map[string]string) string {
	tpl := db.GetConfig("email_template_body")
	if tpl != "" {
		return renderTemplate(tpl, vars)
	}
	safeTitle := html.EscapeString(vars["title"])
	safeStatus := html.EscapeString(vars["status"])
	trackURL := vars["track_url"]
	return fmt.Sprintf(`<html><body style="font-family:-apple-system,sans-serif;color:#333;max-width:600px;margin:0 auto">
<h2>您的反馈状态已更新</h2>
<p><strong>编号：</strong>#%s</p>
<p><strong>标题：</strong>%s</p>
<p><strong>新状态：</strong>%s</p>
<p><a href="%s" style="display:inline-block;padding:10px 20px;background:#e53e3e;color:white;text-decoration:none;border-radius:4px">查看反馈进度</a></p>
<p style="color:#999;font-size:12px;margin-top:24px">此邮件由 FeedShit 自动发送</p>
</body></html>`, vars["id"], safeTitle, safeStatus, trackURL)
}

// BuildReplySubject builds subject for public reply notifications.
func BuildReplySubject(db *database.Database, vars map[string]string) string {
	tpl := db.GetConfig("email_template_subject")
	if tpl != "" {
		return renderTemplate(tpl, vars)
	}
	return fmt.Sprintf("[FeedShit] 反馈 #%s 有新回复", vars["id"])
}

// BuildReplyBody builds HTML body for public reply notifications.
func BuildReplyBody(db *database.Database, vars map[string]string) string {
	tpl := db.GetConfig("email_template_body")
	if tpl != "" {
		return renderTemplate(tpl, vars)
	}
	safeTitle := html.EscapeString(vars["title"])
	safeContent := html.EscapeString(vars["note_content"])
	safeContent = strings.ReplaceAll(safeContent, "\n", "<br>")
	return fmt.Sprintf(`<html><body style="font-family:-apple-system,sans-serif;color:#333;max-width:600px;margin:0 auto">
<h2>您的反馈收到了新回复</h2>
<p><strong>编号：</strong>#%s</p>
<p><strong>标题：</strong>%s</p>
<hr style="border:none;border-top:1px solid #eee;margin:16px 0">
<div style="background:#f9f9f9;padding:12px;border-radius:4px">%s</div>
<p style="color:#999;font-size:12px;margin-top:24px">此邮件由 FeedShit 自动发送</p>
</body></html>`, vars["id"], safeTitle, safeContent)
}

// SendStatusChangeNotification sends an email to the feedback submitter when status changes or a public reply is added.
func (m *Mailer) SendStatusChangeNotification(fb *database.Feedback, subject, htmlBody string) {
	if fb.ContactEmail == "" {
		return
	}

	cfg := getEmailSMTPConfig(m.db)
	host := cfg["smtp_host"]
	port := cfg["smtp_port"]
	user := cfg["smtp_user"]
	pass := cfg["smtp_pass"]
	from := cfg["smtp_from"]

	if host == "" {
		log.Printf("[MAIL] SMTP not configured, skipping submitter notification for feedback #%d", fb.ID)
		return
	}

	if port == "" {
		port = "587"
	}
	if from == "" {
		from = user
	}

	portNum, err := strconv.Atoi(port)
	if err != nil {
		portNum = 587
	}

	safeContact := sanitizeHeader(fb.ContactEmail)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		from, safeContact, sanitizeHeader(subject), htmlBody)

	addr := fmt.Sprintf("%s:%d", host, portNum)
	auth := smtp.PlainAuth("", user, pass, host)
	if err := smtpSend(addr, auth, from, []string{safeContact}, []byte(msg)); err != nil {
		log.Printf("[MAIL] Failed to send submitter notification for feedback #%d: %v", fb.ID, err)
	} else {
		log.Printf("[MAIL] Submitter notification sent for feedback #%d to %s", fb.ID, fb.ContactEmail)
	}
}

// ========== M2 CSAT (Customer Satisfaction) ==========

// BuildCSATSubject builds the subject for a CSAT rating invitation.
func BuildCSATSubject(db *database.Database, vars map[string]string) string {
	tpl := db.GetConfig("email_template_subject")
	if tpl != "" {
		return renderTemplate(tpl, vars)
	}
	return fmt.Sprintf("[FeedShit] 请为反馈 #%s 的服务评分", vars["id"])
}

// BuildCSATBody builds the HTML body for a CSAT rating invitation, with a link to the track page.
func BuildCSATBody(db *database.Database, vars map[string]string) string {
	tpl := db.GetConfig("email_template_body")
	if tpl != "" {
		return renderTemplate(tpl, vars)
	}
	safeTitle := html.EscapeString(vars["title"])
	safeID := html.EscapeString(vars["id"])
	trackURL := vars["track_url"]
	return fmt.Sprintf(`<html><body style="font-family:-apple-system,sans-serif;color:#333;max-width:600px;margin:0 auto">
<h2>您的反馈已处理完毕，请评分</h2>
<p><strong>编号：</strong>#%s</p>
<p><strong>标题：</strong>%s</p>
<p>我们已处理完成您的反馈，点击下方按钮为本次服务打个分：</p>
<p><a href="%s" style="display:inline-block;padding:10px 20px;background:#e53e3e;color:white;text-decoration:none;border-radius:4px">去评分</a></p>
<p style="color:#999;font-size:12px;margin-top:24px">此邮件由 FeedShit 自动发送</p>
</body></html>`, safeID, safeTitle, trackURL)
}

// SendCSATInvite emails a satisfaction-rating invitation to the feedback submitter.
func (m *Mailer) SendCSATInvite(fb *database.Feedback, trackURL string) {
	if fb.ContactEmail == "" {
		return
	}

	cfg := getEmailSMTPConfig(m.db)
	host := cfg["smtp_host"]
	port := cfg["smtp_port"]
	user := cfg["smtp_user"]
	pass := cfg["smtp_pass"]
	from := cfg["smtp_from"]

	if host == "" {
		log.Printf("[MAIL] SMTP not configured, skipping CSAT invite for feedback #%d", fb.ID)
		return
	}
	if port == "" {
		port = "587"
	}
	if from == "" {
		from = user
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		portNum = 587
	}

	vars := map[string]string{
		"id":        fmt.Sprintf("%d", fb.ID),
		"title":     fb.Title,
		"track_url": trackURL,
	}
	subject := BuildCSATSubject(m.db, vars)
	body := BuildCSATBody(m.db, vars)

	safeContact := sanitizeHeader(fb.ContactEmail)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		from, safeContact, sanitizeHeader(subject), body)

	addr := fmt.Sprintf("%s:%d", host, portNum)
	auth := smtp.PlainAuth("", user, pass, host)
	if err := smtpSend(addr, auth, from, []string{safeContact}, []byte(msg)); err != nil {
		log.Printf("[MAIL] Failed to send CSAT invite for feedback #%d: %v", fb.ID, err)
	} else {
		log.Printf("[MAIL] CSAT invite sent for feedback #%d to %s", fb.ID, fb.ContactEmail)
	}
}

// SendSubmitterReplyNotification sends an email to admins when a submitter replies.
func (m *Mailer) SendSubmitterReplyNotification(fb *database.Feedback, replyContent string) {
	cfg := getEmailSMTPConfig(m.db)

	host := cfg["smtp_host"]
	port := cfg["smtp_port"]
	user := cfg["smtp_user"]
	pass := cfg["smtp_pass"]
	from := cfg["smtp_from"]
	to := cfg["smtp_to"]

	if host == "" || to == "" {
		return
	}
	if port == "" {
		port = "587"
	}
	if from == "" {
		from = user
	}
	portNum, _ := strconv.Atoi(port)
	if portNum == 0 {
		portNum = 587
	}

	subject := fmt.Sprintf("[FeedShit] 提交者回复: #%d %s", fb.ID, fb.Title)
	safeTitle := html.EscapeString(fb.Title)
	safeContent := html.EscapeString(replyContent)
	safeContent = strings.ReplaceAll(safeContent, "\n", "<br>")
	adminLink := fmt.Sprintf("%s/admin/#feedback/%d", m.baseURL, fb.ID)

	body := fmt.Sprintf(`<html><body style="font-family:-apple-system,sans-serif;color:#333;max-width:600px;margin:0 auto">
<h2>📬 提交者回复通知</h2>
<p>反馈 <strong>#%d</strong> 的提交者添加了新的回复：</p>
<table style="width:100%%;border-collapse:collapse;margin:16px 0">
<tr><td style="padding:8px;border-bottom:1px solid #eee;font-weight:bold;width:100px">标题</td><td style="padding:8px;border-bottom:1px solid #eee">%s</td></tr>
</table>
<div style="background:#f9f9f9;padding:12px;border-radius:4px">%s</div>
<p><a href="%s" style="display:inline-block;padding:10px 20px;background:#e53e3e;color:white;text-decoration:none;border-radius:4px">在后台查看</a></p>
<p style="color:#999;font-size:12px;margin-top:24px">此邮件由 FeedShit 自动发送</p>
</body></html>`, fb.ID, safeTitle, safeContent, adminLink)

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		from, to, sanitizeHeader(subject), body)

	addr := fmt.Sprintf("%s:%d", host, portNum)
	recipients := strings.Split(to, ",")
	for i := range recipients {
		recipients[i] = strings.TrimSpace(recipients[i])
	}
	auth := smtp.PlainAuth("", user, pass, host)
	if err := smtpSend(addr, auth, from, recipients, []byte(msg)); err != nil {
		log.Printf("[MAIL] Failed to send submitter reply notification for feedback #%d: %v", fb.ID, err)
	} else {
		log.Printf("[MAIL] Submitter reply notification sent for feedback #%d", fb.ID)
	}
}

// Send 发送通用 HTML 邮件。
// to 为逗号分隔的收件人地址。
func (m *Mailer) Send(to, subject, htmlBody string) {
	cfg := getEmailSMTPConfig(m.db)
	host := cfg["smtp_host"]
	port := cfg["smtp_port"]
	user := cfg["smtp_user"]
	pass := cfg["smtp_pass"]
	from := cfg["smtp_from"]

	if host == "" || to == "" {
		log.Printf("[MAIL] SMTP not configured, skipping send to %s", to)
		return
	}

	if port == "" {
		port = "587"
	}
	if from == "" {
		from = user
	}

	portNum, err := strconv.Atoi(port)
	if err != nil {
		portNum = 587
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		from, to, sanitizeHeader(subject), htmlBody)

	addr := fmt.Sprintf("%s:%d", host, portNum)
	recipients := strings.Split(to, ",")
	for i := range recipients {
		recipients[i] = strings.TrimSpace(recipients[i])
	}

	auth := smtp.PlainAuth("", user, pass, host)

	if err := smtpSend(addr, auth, from, recipients, []byte(msg)); err != nil {
		log.Printf("[MAIL] Failed to send to %s: %v", to, err)
	} else {
		log.Printf("[MAIL] Sent to %s, subject=%s", to, subject)
	}
}

// isPermanentSMTPError reports whether err is a permanent 5xx SMTP error that
// will never succeed on retry (e.g., 550 mailbox not found, 553 relay denied).
func isPermanentSMTPError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, code := range []string{"550", "551", "552", "553", "554", "535", "538"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}

// smtpSend 发送 SMTP 邮件，自动处理端口 465 (SSL/TLS) 与 STARTTLS。
// addr: "host:port", auth: SMTP 认证, from: 发件人, to: 收件人列表, msg: 完整的 MIME 消息。
// 发送失败后自动重试 2 次（指数退避：4s、8s），但 5xx 永久错误不重试。
func smtpSend(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt+1)) * time.Second // 2^(attempt+1): 4s, 8s
			time.Sleep(delay)
			log.Printf("[MAIL] retry %d/2 after %v", attempt, delay)
		}
		if err := smtpSendOnce(addr, auth, from, to, msg); err != nil {
			lastErr = err
			log.Printf("[MAIL] attempt %d failed: %v", attempt+1, err)
			// 5xx permanent errors (mailbox not found, relay denied, etc.) will
			// never succeed on retry — bail out immediately.
			if isPermanentSMTPError(err) {
				return fmt.Errorf("permanent SMTP error (no retry): %w", err)
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("all 3 attempts failed, last: %w", lastErr)
}

// smtpSendOnce performs a single SMTP send attempt (no retry).
func smtpSendOnce(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse addr: %w", err)
	}
	portNum, _ := strconv.Atoi(port)
	if portNum == 465 {
		// Port 465 = SMTP over TLS (implicit SSL).
		// SMTP_SKIP_VERIFY=true accepts self-signed certificates (not recommended for production).
		skipVerify := os.Getenv("SMTP_SKIP_VERIFY") == "true"
		tlsCfg := &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: skipVerify,
		}
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("tls dial: %w", err)
		}
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			conn.Close()
			return fmt.Errorf("smtp client: %w", err)
		}
		defer c.Close()
		if err = c.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
		if err = c.Mail(from); err != nil {
			return fmt.Errorf("mail from: %w", err)
		}
		for _, r := range to {
			if err = c.Rcpt(r); err != nil {
				return fmt.Errorf("rcpt %s: %w", r, err)
			}
		}
		w, err := c.Data()
		if err != nil {
			return fmt.Errorf("data: %w", err)
		}
		if _, err = w.Write(msg); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		return w.Close()
	}
	// Port 25/587: STARTTLS via smtp.SendMail.
	// NOTE: Go's smtp.PlainAuth refuses to send credentials over unencrypted connections.
	// If the server does not support STARTTLS, authentication will safely fail rather than
	// transmitting the password in plain text. This is BY DESIGN.
	// For servers that require LOGIN auth (Office 365, etc.), configure port 465 instead.
	return smtp.SendMail(addr, auth, from, to, msg)
}
