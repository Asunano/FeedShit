package report

import (
	"bytes"
	"fmt"
	"html/template"
)

// RenderWeeklyReportHTML 渲染周报 HTML 邮件正文和主题行。
func RenderWeeklyReportHTML(data *ReportData) (subject string, htmlBody string) {
	subject = fmt.Sprintf("[FeedShit] 周报 #%s：共 %d 条新增，%d 条待处理",
		data.WeekNumber, data.TotalNew, data.PendingCount)

	// 安全处理：空切片转为空数组避免模板 nil range
	if data.Categories == nil {
		data.Categories = []CategoryStat{}
	}
	if data.DailyTrend == nil {
		data.DailyTrend = []DailyTrendItem{}
	}
	if data.Projects == nil {
		data.Projects = []ProjectStatItem{}
	}

	var buf bytes.Buffer
	err := tmpl.Execute(&buf, data)
	if err != nil {
		// 渲染失败时降级输出纯文本
		htmlBody = fmt.Sprintf("<p>周报渲染失败: %v</p>", err)
		return
	}
	htmlBody = buf.String()
	return
}

// 内联 CSS 邮件模板，600px 居中，表格布局。
var tmpl = template.Must(template.New("weekly_report").Parse(`
<html>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; color: #333; margin: 0; padding: 0; background: #f5f5f5;">
  <table align="center" width="100%" cellpadding="0" cellspacing="0" style="max-width: 600px; margin: 0 auto; background: #ffffff; border-radius: 8px; overflow: hidden;">
    <tr>
      <td style="padding: 32px 24px 16px; text-align: center; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);">
        <h1 style="color: #ffffff; font-size: 22px; margin: 0; font-weight: 600;">FeedShit 周报</h1>
        <p style="color: rgba(255,255,255,0.85); font-size: 14px; margin: 8px 0 0;">{{.ReportPeriod}}</p>
        <p style="color: rgba(255,255,255,0.7); font-size: 12px; margin: 4px 0 0;">生成时间：{{.GeneratedAt}} ｜ 周号：{{.WeekNumber}}</p>
      </td>
    </tr>

    <!-- 概览卡片 -->
    <tr>
      <td style="padding: 24px 24px 8px;">
        <table width="100%" cellpadding="0" cellspacing="0">
          <tr>
            <td width="20%" style="text-align: center; padding: 12px 4px;">
              <div style="font-size: 28px; font-weight: 700; color: #333;">{{.TotalNew}}</div>
              <div style="font-size: 11px; color: #888; margin-top: 4px;">本周新增</div>
            </td>
            <td width="20%" style="text-align: center; padding: 12px 4px;">
              <div style="font-size: 28px; font-weight: 700; color: #e53e3e;">{{.PendingCount}}</div>
              <div style="font-size: 11px; color: #888; margin-top: 4px;">待处理</div>
            </td>
            <td width="20%" style="text-align: center; padding: 12px 4px;">
              <div style="font-size: 28px; font-weight: 700; color: #3182ce;">{{.ProcessingCount}}</div>
              <div style="font-size: 11px; color: #888; margin-top: 4px;">处理中</div>
            </td>
            <td width="20%" style="text-align: center; padding: 12px 4px;">
              <div style="font-size: 28px; font-weight: 700; color: #38a169;">{{.ResolvedCount}}</div>
              <div style="font-size: 11px; color: #888; margin-top: 4px;">已解决</div>
            </td>
            <td width="20%" style="text-align: center; padding: 12px 4px;">
              <div style="font-size: 28px; font-weight: 700; color: #a0aec0;">{{.ClosedCount}}</div>
              <div style="font-size: 11px; color: #888; margin-top: 4px;">已关闭</div>
            </td>
          </tr>
        </table>
      </td>
    </tr>

    <tr><td style="border-bottom: 1px solid #eee; margin: 0 24px;"></td></tr>

    <!-- 分类分布 -->
    <tr>
      <td style="padding: 16px 24px 8px;">
        <h2 style="font-size: 15px; color: #555; margin: 0 0 12px; font-weight: 600;">📊 分类分布</h2>
        <table width="100%" cellpadding="0" cellspacing="0">
          {{range .Categories}}
          <tr>
            <td style="padding: 6px 8px; font-size: 13px; color: #555; width: 40%;">{{.Name}}</td>
            <td style="padding: 6px 8px; font-size: 13px; color: #333; width: 20%; text-align: right;">{{.Count}}</td>
            <td style="padding: 6px 8px; width: 40%;">
              <div style="background: #e2e8f0; border-radius: 4px; height: 16px; overflow: hidden;">
                <div style="background: linear-gradient(90deg, #667eea, #764ba2); height: 100%; width: {{printf "%.0f" .Percent}}%; border-radius: 4px;"></div>
              </div>
            </td>
          </tr>
          {{else}}
          <tr><td style="padding: 12px 8px; font-size: 13px; color: #aaa; text-align: center;">暂无分类数据</td></tr>
          {{end}}
        </table>
      </td>
    </tr>

    <tr><td style="border-bottom: 1px solid #eee; margin: 0 24px;"></td></tr>

    <!-- 每日趋势 -->
    <tr>
      <td style="padding: 16px 24px 8px;">
        <h2 style="font-size: 15px; color: #555; margin: 0 0 12px; font-weight: 600;">📈 每日趋势</h2>
        <table width="100%" cellpadding="0" cellspacing="0">
          {{range .DailyTrend}}
          <tr>
            <td style="padding: 4px 8px; font-size: 13px; color: #888; width: 44px;">{{.Date}}</td>
            <td style="padding: 4px 4px; font-size: 13px; color: #888; width: 20px;">周{{.Weekday}}</td>
            <td style="padding: 4px 8px; font-size: 12px; color: #667eea; letter-spacing: 1px; font-family: 'Courier New', monospace;">{{.Bar}}</td>
            <td style="padding: 4px 8px; font-size: 13px; color: #333; width: 40px; text-align: right;">{{.Count}}</td>
          </tr>
          {{else}}
          <tr><td style="padding: 12px 8px; font-size: 13px; color: #aaa; text-align: center;">暂无每日数据</td></tr>
          {{end}}
        </table>
      </td>
    </tr>

    <tr><td style="border-bottom: 1px solid #eee; margin: 0 24px;"></td></tr>

    <!-- 项目概况 -->
    <tr>
      <td style="padding: 16px 24px 8px;">
        <h2 style="font-size: 15px; color: #555; margin: 0 0 12px; font-weight: 600;">📁 项目概况（{{.ProjectCount}} 个项目）</h2>
        <table width="100%" cellpadding="0" cellspacing="0" style="border-collapse: collapse;">
          <tr>
            <td style="padding: 8px; font-size: 12px; color: #888; border-bottom: 2px solid #eee; font-weight: 600;">项目</td>
            <td style="padding: 8px; font-size: 12px; color: #888; border-bottom: 2px solid #eee; font-weight: 600; text-align: right;">本周反馈</td>
            <td style="padding: 8px; font-size: 12px; color: #888; border-bottom: 2px solid #eee; font-weight: 600; text-align: right;">最新</td>
          </tr>
          {{range .Projects}}
          <tr>
            <td style="padding: 6px 8px; font-size: 13px; color: #333; border-bottom: 1px solid #f0f0f0;">{{.ProjectName}}</td>
            <td style="padding: 6px 8px; font-size: 13px; color: #333; border-bottom: 1px solid #f0f0f0; text-align: right;">{{.Count}}</td>
            <td style="padding: 6px 8px; font-size: 12px; color: #888; border-bottom: 1px solid #f0f0f0; text-align: right;">{{.LatestAt}}</td>
          </tr>
          {{else}}
          <tr><td style="padding: 12px 8px; font-size: 13px; color: #aaa; text-align: center;" colspan="3">暂无项目数据</td></tr>
          {{end}}
        </table>
      </td>
    </tr>

    <!-- 底部 -->
    <tr>
      <td style="padding: 24px; text-align: center; background: #f9f9f9;">
        <p style="color: #aaa; font-size: 11px; margin: 0; line-height: 1.6;">
          此邮件由 FeedShit 系统自动发送<br>
          如需修改收件人，请在系统设置中更新 report_recipients 配置项
        </p>
      </td>
    </tr>
  </table>
</body>
</html>
`))
