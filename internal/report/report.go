// Package report 提供每周周报的统计采集、HTML 渲染与邮件发送功能。
package report

import (
	"fmt"
	"log"
	"strings"
	"time"

	"feedshit/internal/database"
	"feedshit/internal/email"
)

// ReportData 周报的全部统计数据。
type ReportData struct {
	ReportPeriod    string            // 统计周期描述，如 "2026-07-14 (周一) ~ 2026-07-20 (周日)"
	GeneratedAt     string            // 生成时间，如 "2026-07-21 08:00"
	WeekNumber      string            // ISO 周号，如 "2026-W29"
	TotalNew        int               // 本周新增总数
	PendingCount    int               // 待处理数
	ProcessingCount int               // 处理中数
	ResolvedCount   int               // 已解决数
	ClosedCount     int               // 已关闭数
	ProjectCount    int               // 涉及项目数
	Categories      []CategoryStat    // 分类分布
	DailyTrend      []DailyTrendItem  // 每日趋势
	Projects        []ProjectStatItem // 各项目概况
}

// CategoryStat 分类统计项。
type CategoryStat struct {
	Name    string  // 分类名称
	Count   int     // 该分类反馈数
	Percent float64 // 占比百分比
}

// DailyTrendItem 每日趋势项。
type DailyTrendItem struct {
	Date    string // 日期 "07/14"
	Weekday string // 星期 "一"
	Count   int    // 当日反馈数
	Bar     string // 条形图 "████████"
}

// ProjectStatItem 项目统计项。
type ProjectStatItem struct {
	ProjectID   string // 项目 ID（slug）
	ProjectName string // 项目名称
	Count       int    // 本周反馈数
	LatestAt    string // 最新反馈时间 "07/20 18:30"
}

// 中文星期名映射
var weekdayNames = map[time.Weekday]string{
	time.Monday:    "一",
	time.Tuesday:   "二",
	time.Wednesday: "三",
	time.Thursday:  "四",
	time.Friday:    "五",
	time.Saturday:  "六",
	time.Sunday:    "日",
}

// toInt 安全地将 interface{} 中的 int/int64 数值转为 int。
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// computeWeekRange 返回上周一 00:00 ~ 上周日 23:59 的 Unix 时间戳（本地时间）。
func computeWeekRange() (startUnix, endUnix int64, startTime, endTime time.Time) {
	now := time.Now()
	// 本周一的偏移量：周一=0，周二=1，...，周日=6
	offset := (int(now.Weekday()) + 6) % 7
	thisMonday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -offset)
	// 上周一 = 本周一 - 7 天
	lastMonday := thisMonday.AddDate(0, 0, -7)
	lastSunday := lastMonday.AddDate(0, 0, 6)

	startTime = lastMonday
	endTime = time.Date(lastSunday.Year(), lastSunday.Month(), lastSunday.Day(), 23, 59, 59, 0, lastSunday.Location())
	startUnix = startTime.Unix()
	endUnix = endTime.Unix()
	return
}

// collectWeeklyStats 采集上周的统计数据并组装为 ReportData。
func collectWeeklyStats(db *database.Database) (*ReportData, error) {
	startUnix, endUnix, startTime, endTime := computeWeekRange()

	// 获取总数和项目数
	total, projectCount, err := db.GetWeeklyStats(startUnix, endUnix)
	if err != nil {
		return nil, fmt.Errorf("获取周统计失败: %w", err)
	}

	// 获取状态分布
	statusDist, err := db.GetWeeklyStatusDistribution(startUnix, endUnix)
	if err != nil {
		return nil, fmt.Errorf("获取状态分布失败: %w", err)
	}
	pendingCount := statusDist["pending"]
	processingCount := statusDist["processing"]
	resolvedCount := statusDist["resolved"]
	closedCount := statusDist["closed"]

	// 获取分类统计
	categoryCounts, err := db.GetWeeklyCategoryCounts(startUnix, endUnix)
	if err != nil {
		return nil, fmt.Errorf("获取分类统计失败: %w", err)
	}
	var categories []CategoryStat
	totalForPercent := total
	if totalForPercent == 0 {
		totalForPercent = 1 // 避免除零
	}
	for name, count := range categoryCounts {
		if name == "" {
			name = "未分类"
		}
		percent := float64(count) / float64(totalForPercent) * 100
		categories = append(categories, CategoryStat{
			Name:    name,
			Count:   count,
			Percent: percent,
		})
	}

	// 获取每日趋势
	trendData, err := db.GetDailyTrendInRange(startUnix, endUnix)
	if err != nil {
		return nil, fmt.Errorf("获取每日趋势失败: %w", err)
	}
	// 计算最大日反馈数（用于条形图比例）
	maxDaily := 0
	for _, d := range trendData {
		if cnt := toInt(d["count"]); cnt > maxDaily {
			maxDaily = cnt
		}
	}
	var dailyTrend []DailyTrendItem
	for _, d := range trendData {
		dayStr, _ := d["date"].(string)
		cnt := toInt(d["count"])
		// 解析日期 "2006-01-02" 格式
		parsed, err := time.Parse("2006-01-02", dayStr)
		dateDisplay := dayStr
		weekday := ""
		if err == nil {
			dateDisplay = parsed.Format("01/02")
			weekday = weekdayNames[parsed.Weekday()]
		}
		// 条形图：最大 20 个█
		barLen := 0
		if maxDaily > 0 {
			barLen = cnt * 20 / maxDaily
		}
		if barLen > 20 {
			barLen = 20
		}
		bar := strings.Repeat("█", barLen)

		dailyTrend = append(dailyTrend, DailyTrendItem{
			Date:    dateDisplay,
			Weekday: weekday,
			Count:   cnt,
			Bar:     bar,
		})
	}

	// 获取项目统计
	projectData, err := db.GetWeeklyProjectStats(startUnix, endUnix)
	if err != nil {
		return nil, fmt.Errorf("获取项目统计失败: %w", err)
	}
	var projects []ProjectStatItem
	for _, p := range projectData {
		projectID, _ := p["project_id"].(string)
		projectName, _ := p["project_name"].(string)
		cnt := toInt(p["count"])
		latestAt, _ := p["latest_at"].(string)
		// 格式化最新时间为 "07/20 18:30"
		latestDisplay := latestAt
		if parsed, err := time.Parse("2006-01-02 15:04:05", latestAt); err == nil {
			latestDisplay = parsed.Format("01/02 15:04")
		}
		projects = append(projects, ProjectStatItem{
			ProjectID:   projectID,
			ProjectName: projectName,
			Count:       cnt,
			LatestAt:    latestDisplay,
		})
	}

	// 计算 ISO 周号
	year, week := startTime.ISOWeek()
	weekNumber := fmt.Sprintf("%d-W%02d", year, week)

	// 格式化周期描述
	period := fmt.Sprintf("%s (%s) ~ %s (%s)",
		startTime.Format("2006-01-02"), weekdayNames[startTime.Weekday()],
		endTime.Format("2006-01-02"), weekdayNames[endTime.Weekday()])

	generatedAt := time.Now().Format("2006-01-02 15:04")

	data := &ReportData{
		ReportPeriod:    period,
		GeneratedAt:     generatedAt,
		WeekNumber:      weekNumber,
		TotalNew:        total,
		PendingCount:    pendingCount,
		ProcessingCount: processingCount,
		ResolvedCount:   resolvedCount,
		ClosedCount:     closedCount,
		ProjectCount:    projectCount,
		Categories:      categories,
		DailyTrend:      dailyTrend,
		Projects:        projects,
	}

	log.Printf("[REPORT] 周报数据采集完成：周期=%s, 新增=%d, 待处理=%d, 处理中=%d, 已解决=%d, 已关闭=%d",
		period, total, pendingCount, processingCount, resolvedCount, closedCount)
	return data, nil
}

// GenerateWeeklyReport 采集周报数据、渲染 HTML 并发送邮件。
// 返回首个错误；内部关键节点均有日志输出。
func GenerateWeeklyReport(db *database.Database, mailer *email.Mailer) error {
	log.Println("[REPORT] 开始生成周报...")

	data, err := collectWeeklyStats(db)
	if err != nil {
		return fmt.Errorf("采集周报数据失败: %w", err)
	}

	subject, htmlBody := RenderWeeklyReportHTML(data)
	log.Printf("[REPORT] 邮件模板渲染完成，主题=%s", subject)

	recipients := db.GetConfig("report_recipients")
	if recipients == "" {
		log.Println("[REPORT] 未配置周报收件人（report_recipients），跳过发送")
		return nil
	}

	log.Printf("[REPORT] 发送周报到 %s ...", recipients)
	mailer.Send(recipients, subject, htmlBody)

	if err := db.InsertAuditLog("weekly_report", "sent to "+recipients, "system", ""); err != nil {
		log.Printf("[REPORT] 写入审计日志失败: %v", err)
		// 不阻断主流程
	}

	log.Println("[REPORT] 周报发送完成")
	return nil
}
