package report

import (
	"strings"
	"testing"
	"time"

	"feedshit/internal/database"
	"feedshit/internal/email"
)

// setupReportDB creates a fresh in-memory DB for report tests.
func setupReportDB(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	return db
}

// insertFeedback inserts a feedback with the given project_id, status, category and created_at (unix timestamp).
func insertFeedback(t *testing.T, db *database.Database, projectID, title, status, category string, createdAt int64) {
	t.Helper()
	_, err := db.ExecRaw(
		`INSERT INTO feedbacks (project_id, title, status, category, created_at) VALUES (?, ?, ?, ?, ?)`,
		projectID, title, status, category, createdAt,
	)
	if err != nil {
		t.Fatalf("插入测试反馈失败: %v", err)
	}
}

// insertProject inserts a project for JOIN testing.
func insertProject(t *testing.T, db *database.Database, slug, name string) {
	t.Helper()
	_, err := db.ExecRaw(
		`INSERT INTO projects (slug, name) VALUES (?, ?)`,
		slug, name,
	)
	if err != nil {
		t.Fatalf("插入测试项目失败: %v", err)
	}
}

// weekTimestamps computes the last-week range timestamps for test data placement.
// Returns (lastMonday 00:00, lastSunday 23:59) unix timestamps.
func weekTimestamps() (lastMondayUnix, lastSundayUnix int64) {
	now := time.Now()
	offset := (int(now.Weekday()) + 6) % 7
	thisMonday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -offset)
	lastMonday := thisMonday.AddDate(0, 0, -7)
	lastSunday := lastMonday.AddDate(0, 0, 6)
	lastMondayUnix = lastMonday.Unix()
	lastSundayUnix = time.Date(lastSunday.Year(), lastSunday.Month(), lastSunday.Day(), 23, 59, 59, 0, lastSunday.Location()).Unix()
	return
}

// ========== RenderWeeklyReportHTML 测试 ==========

// TestRenderWeeklyReportHTML_SubjectFormat 验证主题格式（第 7 项）。
func TestRenderWeeklyReportHTML_SubjectFormat(t *testing.T) {
	data := &ReportData{
		WeekNumber:    "2026-W29",
		TotalNew:      128,
		PendingCount:  23,
		Categories:    []CategoryStat{},
		DailyTrend:    []DailyTrendItem{},
		Projects:      []ProjectStatItem{},
	}
	subject, _ := RenderWeeklyReportHTML(data)

	expected := "[FeedShit] 周报 #2026-W29：共 128 条新增，23 条待处理"
	if subject != expected {
		t.Fatalf("主题格式错误:\ngot:  %q\nwant: %q", subject, expected)
	}
}

// TestRenderWeeklyReportHTML_HtmlBodySections 验证 HTML 包含所有数据段（第 7 项）。
func TestRenderWeeklyReportHTML_HtmlBodySections(t *testing.T) {
	data := &ReportData{
		ReportPeriod:    "2026-07-14 (周一) ~ 2026-07-20 (周日)",
		GeneratedAt:     "2026-07-21 08:00",
		WeekNumber:      "2026-W29",
		TotalNew:        128,
		PendingCount:    23,
		ProcessingCount: 15,
		ResolvedCount:   80,
		ClosedCount:     10,
		ProjectCount:    5,
		Categories: []CategoryStat{
			{Name: "功能请求", Count: 60, Percent: 46.9},
			{Name: "Bug报告", Count: 40, Percent: 31.2},
		},
		DailyTrend: []DailyTrendItem{
			{Date: "07/14", Weekday: "一", Count: 15, Bar: "███████"},
			{Date: "07/15", Weekday: "二", Count: 22, Bar: "███████████"},
		},
		Projects: []ProjectStatItem{
			{ProjectID: "acme", ProjectName: "Acme Corp", Count: 50, LatestAt: "07/20 18:30"},
			{ProjectID: "beta", ProjectName: "Beta App", Count: 30, LatestAt: "07/19 15:00"},
		},
	}

	_, htmlBody := RenderWeeklyReportHTML(data)

	// 验证各数据段存在
	sections := []string{
		"2026-07-14 (周一) ~ 2026-07-20 (周日)",
		"2026-07-21 08:00",
		"2026-W29",
		"128",  // TotalNew
		"23",   // PendingCount (with color #e53e3e)
		"15",   // ProcessingCount (with color #3182ce)
		"80",   // ResolvedCount (with color #38a169)
		"10",   // ClosedCount (with color #a0aec0)
		"5",    // ProjectCount
		"功能请求",
		"Bug报告",
		"07/14",
		"07/15",
		"周一",
		"周二",
		"Acme Corp",
		"Beta App",
	}
	for _, s := range sections {
		if !strings.Contains(htmlBody, s) {
			t.Fatalf("HTML 缺少内容: %q", s)
		}
	}

	// 验证内联 CSS 颜色语义
	colorChecks := []string{
		"#e53e3e", // pending
		"#3182ce", // processing
		"#38a169", // resolved
		"#a0aec0", // closed
	}
	for _, c := range colorChecks {
		if !strings.Contains(htmlBody, c) {
			t.Fatalf("HTML 缺少颜色样式: %s", c)
		}
	}

	// 验证是合法 HTML 片段
	if !strings.Contains(htmlBody, "<html>") {
		t.Fatal("HTML 应包含 <html> 标签")
	}
	if !strings.Contains(htmlBody, "</html>") {
		t.Fatal("HTML 应包含 </html> 标签")
	}
}

// TestRenderWeeklyReportHTML_NilSlices 验证空切片不导致模板 panic（第 7 项边界条件）。
func TestRenderWeeklyReportHTML_NilSlices(t *testing.T) {
	data := &ReportData{
		WeekNumber:  "2026-W30",
		TotalNew:    0,
		Categories:  nil,
		DailyTrend:  nil,
		Projects:    nil,
	}
	subject, htmlBody := RenderWeeklyReportHTML(data)

	if subject == "" {
		t.Fatal("主题不应为空")
	}
	if !strings.Contains(htmlBody, "暂无分类数据") {
		t.Fatal("空分类应显示占位文本")
	}
	if !strings.Contains(htmlBody, "暂无每日数据") {
		t.Fatal("空每日趋势应显示占位文本")
	}
	if !strings.Contains(htmlBody, "暂无项目数据") {
		t.Fatal("空项目应显示占位文本")
	}
}

// ========== collectWeeklyStats 测试 ==========

// TestCollectWeeklyStats_DataFiltering 验证自然周范围正确、各字段正确填充（第 6 项）。
func TestCollectWeeklyStats_DataFiltering(t *testing.T) {
	db := setupReportDB(t)

	// 插入项目
	insertProject(t, db, "acme", "Acme Corp")
	insertProject(t, db, "beta", "Beta App")

	lastMon, _ := weekTimestamps()
	// 上周一 中午
	midLastMon := lastMon + 43200
	// 上周三 中午
	wedLastWeek := lastMon + 2*86400 + 43200
	// 上周六 中午
	satLastWeek := lastMon + 5*86400 + 43200

	// 本周内的数据（不应被统计）
	thisMonday := lastMon + 7*86400
	thisWed := thisMonday + 2*86400

	// 插入上周数据（应在周报范围内）
	insertFeedback(t, db, "acme", "反馈1-待处理", "pending", "bug", midLastMon)
	insertFeedback(t, db, "acme", "反馈2-处理中", "processing", "feature", wedLastWeek)
	insertFeedback(t, db, "beta", "反馈3-已解决", "resolved", "bug", satLastWeek)
	insertFeedback(t, db, "beta", "反馈4-已关闭", "closed", "feature", midLastMon)
	insertFeedback(t, db, "acme", "反馈5-待处理2", "pending", "ui", midLastMon)

	// 插入本周数据（不应被统计）
	insertFeedback(t, db, "acme", "本周反馈不应统计", "pending", "other", thisMonday)
	insertFeedback(t, db, "beta", "本周反馈2", "pending", "other", thisWed)

	// 调用 collectWeeklyStats
	data, err := collectWeeklyStats(db)
	if err != nil {
		t.Fatalf("collectWeeklyStats 失败: %v", err)
	}

	// 验证总数：只统计上周的 5 条
	if data.TotalNew != 5 {
		t.Fatalf("TotalNew = %d, 期望 5", data.TotalNew)
	}

	// 验证项目数：2 个不同项目
	if data.ProjectCount != 2 {
		t.Fatalf("ProjectCount = %d, 期望 2", data.ProjectCount)
	}

	// 验证状态分布
	if data.PendingCount != 2 { // 反馈1 + 反馈5
		t.Fatalf("PendingCount = %d, 期望 2", data.PendingCount)
	}
	if data.ProcessingCount != 1 {
		t.Fatalf("ProcessingCount = %d, 期望 1", data.ProcessingCount)
	}
	if data.ResolvedCount != 1 {
		t.Fatalf("ResolvedCount = %d, 期望 1", data.ResolvedCount)
	}
	if data.ClosedCount != 1 {
		t.Fatalf("ClosedCount = %d, 期望 1", data.ClosedCount)
	}

	// 验证分类分布
	if len(data.Categories) != 3 {
		t.Fatalf("Categories 数量 = %d, 期望 3", len(data.Categories))
	}
	catMap := make(map[string]int)
	for _, c := range data.Categories {
		catMap[c.Name] = c.Count
	}
	if catMap["bug"] != 2 {
		t.Fatalf("bug 分类 count = %d, 期望 2", catMap["bug"])
	}
	if catMap["feature"] != 2 {
		t.Fatalf("feature 分类 count = %d, 期望 2", catMap["feature"])
	}
	if catMap["ui"] != 1 {
		t.Fatalf("ui 分类 count = %d, 期望 1", catMap["ui"])
	}

	// 验证每日趋势：应有 3 天有数据
	if len(data.DailyTrend) != 3 {
		t.Fatalf("DailyTrend 天数 = %d, 期望 3", len(data.DailyTrend))
	}

	// 验证项目统计：2 个项目
	if len(data.Projects) != 2 {
		t.Fatalf("Projects 数量 = %d, 期望 2", len(data.Projects))
	}
	for _, p := range data.Projects {
		if p.ProjectID == "acme" {
			if p.Count != 3 { // 反馈1, 反馈2, 反馈5
				t.Fatalf("acme 项目 count = %d, 期望 3", p.Count)
			}
			if p.ProjectName != "Acme Corp" {
				t.Fatalf("acme 项目名 = %q, 期望 'Acme Corp'", p.ProjectName)
			}
		}
		if p.ProjectID == "beta" {
			if p.Count != 2 { // 反馈3, 反馈4
				t.Fatalf("beta 项目 count = %d, 期望 2", p.Count)
			}
		}
	}

	// 验证 ReportPeriod 包含起止日期
	if !strings.Contains(data.ReportPeriod, "~") {
		t.Fatalf("ReportPeriod 格式异常: %q", data.ReportPeriod)
	}
	// 验证有效日期格式 YYYY-MM-DD (X) ~ YYYY-MM-DD (X)
	parts := strings.Split(data.ReportPeriod, " ~ ")
	if len(parts) != 2 {
		t.Fatalf("ReportPeriod 应包含 ~ 分隔: %q", data.ReportPeriod)
	}

	// 验证 WeekNumber 是 ISO 周号格式
	if !strings.Contains(data.WeekNumber, "-W") {
		t.Fatalf("WeekNumber 格式异常: %q", data.WeekNumber)
	}
}

// TestCollectWeeklyStats_EmptyData 验证无数据时返回零值而非错误（第 6 项边界条件）。
func TestCollectWeeklyStats_EmptyData(t *testing.T) {
	db := setupReportDB(t)

	data, err := collectWeeklyStats(db)
	if err != nil {
		t.Fatalf("空数据时 collectWeeklyStats 不应报错: %v", err)
	}
	if data == nil {
		t.Fatal("返回的 data 不应为 nil")
	}
	if data.TotalNew != 0 {
		t.Fatalf("TotalNew = %d, 期望 0", data.TotalNew)
	}
}

// ========== GenerateWeeklyReport 测试 ==========

// TestGenerateWeeklyReport_NoRecipients 验证收件人为空时跳过发送（第 4、8 项）。
func TestGenerateWeeklyReport_NoRecipients(t *testing.T) {
	db := setupReportDB(t)
	mailer := email.NewMailer(db, "http://test")

	// 不设置 report_recipients，应跳过发送
	err := GenerateWeeklyReport(db, mailer)
	if err != nil {
		t.Fatalf("收件人为空时 GenerateWeeklyReport 不应报错: %v", err)
	}

	// 验证未写入审计日志
	rows, err := db.QueryRaw(`SELECT COUNT(*) FROM audit_logs WHERE action = 'weekly_report'`)
	if err != nil {
		t.Fatalf("查询审计日志失败: %v", err)
	}
	defer rows.Close()
	var count int
	if rows.Next() {
		rows.Scan(&count)
	}
	if count > 0 {
		t.Fatal("收件人为空时不应写入审计日志")
	}
}

// TestGenerateWeeklyReport_WithRecipients 验证有收件人时正常发送并记录审计日志（第 8、9 项）。
func TestGenerateWeeklyReport_WithRecipients(t *testing.T) {
	db := setupReportDB(t)
	mailer := email.NewMailer(db, "http://test")

	// 插入一些测试数据
	lastMon, _ := weekTimestamps()
	insertFeedback(t, db, "test-proj", "测试反馈", "pending", "bug", lastMon+3600)
	insertProject(t, db, "test-proj", "测试项目")

	// 设置收件人（SMTP 未配置时 mailer.Send 会 log 并跳过，不影响主流程）
	if err := db.SetConfig("report_recipients", "admin@example.com", "周报收件人"); err != nil {
		t.Fatalf("SetConfig 失败: %v", err)
	}

	err := GenerateWeeklyReport(db, mailer)
	if err != nil {
		t.Fatalf("GenerateWeeklyReport 失败: %v", err)
	}

	// 验证审计日志已写入
	rows, err := db.QueryRaw(`SELECT action, detail, user FROM audit_logs WHERE action = 'weekly_report' ORDER BY id DESC LIMIT 1`)
	if err != nil {
		t.Fatalf("查询审计日志失败: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("应有审计日志记录")
	}
	var action, detail, user string
	if err := rows.Scan(&action, &detail, &user); err != nil {
		t.Fatalf("读取审计日志失败: %v", err)
	}
	if action != "weekly_report" {
		t.Fatalf("action = %q, 期望 'weekly_report'", action)
	}
	if !strings.Contains(detail, "admin@example.com") {
		t.Fatalf("detail 应包含收件人: %q", detail)
	}
	if user != "system" {
		t.Fatalf("user = %q, 期望 'system'", user)
	}
}
