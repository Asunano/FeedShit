package email

import (
	"fmt"
	"html"
	"log"
	"net/smtp"
	"strconv"
	"strings"

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

// SendFeedbackNotification sends an email notification for a new feedback.
func (m *Mailer) SendFeedbackNotification(fb *database.Feedback) {
	cfg := getEmailConfig(m.db)

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

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		from, to, subject, body)

	addr := fmt.Sprintf("%s:%d", host, portNum)

	recipients := strings.Split(to, ",")
	for i := range recipients {
		recipients[i] = strings.TrimSpace(recipients[i])
	}

	auth := smtp.PlainAuth("", user, pass, host)
	if err := smtp.SendMail(addr, auth, from, recipients, []byte(msg)); err != nil {
		log.Printf("[MAIL] Failed to send notification for feedback #%d: %v", fb.ID, err)
	} else {
		log.Printf("[MAIL] Notification sent for feedback #%d to %s", fb.ID, to)
	}
}

func getEmailConfig(db *database.Database) map[string]string {
	configs, err := db.GetAllConfig()
	if err != nil {
		return map[string]string{}
	}
	m := make(map[string]string, len(configs))
	for _, c := range configs {
		m[c.Key] = c.Value
	}
	return m
}
