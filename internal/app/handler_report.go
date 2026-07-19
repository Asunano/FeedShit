package app

import (
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"

	"feedshit/internal/database"
	"feedshit/internal/middleware"
)

func (a *App) AdminStats(c *gin.Context) {
	// Apply member_grants restrictions for non-admin roles
	total, projects, today, err := a.getScopedStats(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取统计失败"})
		return
	}
	csatAvg, csatTotal, _, cerr := a.DB.GetCSATStats()
	if cerr != nil {
		csatAvg, csatTotal = 0, 0
	}
	c.JSON(http.StatusOK, gin.H{
		"total_feedbacks": total,
		"total_projects":  projects,
		"today_feedbacks": today,
		"csat_avg":        csatAvg,
		"csat_total":      csatTotal,
	})
}

// ========== Admin: Project Stats ==========

func (a *App) AdminProjectStats(c *gin.Context) {
	projectIDs := a.getAdminProjectIDs(c)
	stats, err := a.DB.GetProjectStatsForProjects(projectIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取统计失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

// ========== Admin: CSV Export ==========

func (a *App) AdminExportCSV(c *gin.Context) {
	projectID := c.Query("project")

	// RBAC: non-admin users can only export projects they have access to
	roleStr, _ := c.Get("admin_role")
	if roleStr != "admin" {
		allowedIDs := a.getAdminProjectIDs(c)
		if projectID != "" {
			// Verify the requested project is in the allowed list
			allowed := false
			for _, id := range allowedIDs {
				if id == projectID {
					allowed = true
					break
				}
			}
			if !allowed {
				c.JSON(http.StatusForbidden, gin.H{"error": "无权导出该项目"})
				return
			}
		} else {
			// No specific project → only export accessible projects
			// ExportFeedbacks with empty string exports ALL, which we don't want
			// Collect feedbacks from all allowed projects
			allFeedbacks := []database.Feedback{}
			for _, pid := range allowedIDs {
				fbs, err := a.DB.ExportFeedbacks(pid)
				if err == nil {
					allFeedbacks = append(allFeedbacks, fbs...)
				}
			}
			feedbacks := allFeedbacks

			user, _ := c.Get("admin_user")
			clientIP := middleware.GetClientIP(c)
			a.DB.InsertAuditLog("export", fmt.Sprintf("导出反馈 %d 条 (项目: %s)", len(feedbacks), projectID), fmt.Sprintf("%v", user), clientIP)

			isAdmin := roleStr == "admin"
			switch c.Query("fmt") {
			case "json":
				a.exportJSON(c, projectID, feedbacks, isAdmin)
				return
			case "xlsx":
				a.exportXLSX(c, projectID, feedbacks, isAdmin)
				return
			default:
				a.exportCSV(c, projectID, feedbacks, isAdmin)
			}
			return
		}
	}

	feedbacks, err := a.DB.ExportFeedbacks(projectID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "导出失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("export", fmt.Sprintf("导出反馈 %d 条 (项目: %s)", len(feedbacks), projectID), fmt.Sprintf("%v", user), clientIP)

	isAdmin := roleStr == "admin"
	// M12: support json / xlsx export formats
	switch c.Query("fmt") {
	case "json":
		a.exportJSON(c, projectID, feedbacks, isAdmin)
		return
	case "xlsx":
		a.exportXLSX(c, projectID, feedbacks, isAdmin)
		return
	default:
		a.exportCSV(c, projectID, feedbacks, isAdmin)
	}
}

func (a *App) exportCSV(c *gin.Context, projectID string, feedbacks []database.Feedback, isAdmin bool) {
	filename := "feedbacks"
	if projectID != "" {
		filename = "feedbacks_" + projectID
	}
	filename += "_" + time.Now().Format("20060102_150405") + ".csv"

	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "text/csv; charset=utf-8")

	w := csv.NewWriter(c.Writer)
	// Write BOM for Excel compatibility
	c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	w.Write([]string{"ID", "项目", "标题", "描述", "自定义字段", "附件", "状态", "标签", "指派", "联系人", "联系邮箱", "来源IP", "提交时间"})
	for _, fb := range feedbacks {
		clientIP := fb.ClientIP
		if !isAdmin && clientIP != "" {
			clientIP = "已隐藏"
		}
		w.Write([]string{
			strconv.FormatInt(fb.ID, 10),
			fb.ProjectID,
			fb.Title,
			fb.Description,
			fb.CustomData,
			fb.FilePaths,
			fb.Status,
			fb.Tags,
			fb.Assignee,
			fb.ContactName,
			fb.ContactEmail,
			clientIP,
			fb.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	w.Flush()
}

func (a *App) exportJSON(c *gin.Context, projectID string, feedbacks []database.Feedback, isAdmin bool) {
	filename := "feedbacks"
	if projectID != "" {
		filename = "feedbacks_" + projectID
	}
	filename += "_" + time.Now().Format("20060102_150405") + ".json"

	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "application/json; charset=utf-8")

	// Mask client_ip for non-admin users
	if !isAdmin {
		sanitized := make([]interface{}, len(feedbacks))
		for i, fb := range feedbacks {
			type safeFeedback struct {
				database.Feedback
				ClientIP string `json:"client_ip"`
			}
			safe := safeFeedback{Feedback: fb}
			if fb.ClientIP != "" {
				safe.ClientIP = "已隐藏"
			} else {
				safe.ClientIP = fb.ClientIP
			}
			sanitized[i] = safe
		}
		if err := json.NewEncoder(c.Writer).Encode(sanitized); err != nil {
			log.Printf("[EXPORT] JSON encode failed: %v", err)
		}
		return
	}

	if err := json.NewEncoder(c.Writer).Encode(feedbacks); err != nil {
		log.Printf("[EXPORT] JSON encode failed: %v", err)
	}
}

func (a *App) exportXLSX(c *gin.Context, projectID string, feedbacks []database.Feedback, isAdmin bool) {
	filename := "feedbacks"
	if projectID != "" {
		filename = "feedbacks_" + projectID
	}
	filename += "_" + time.Now().Format("20060102_150405") + ".xlsx"

	f := excelize.NewFile()
	sheet := "Feedbacks"
	f.SetSheetName("Sheet1", sheet)
	headers := []string{"ID", "项目", "标题", "描述", "自定义字段", "附件", "状态", "标签", "指派", "联系人", "联系邮箱", "来源IP", "提交时间"}
	f.SetSheetRow(sheet, "A1", &headers)
	for i, fb := range feedbacks {
		clientIP := fb.ClientIP
		if !isAdmin && clientIP != "" {
			clientIP = "已隐藏"
		}
		row := []interface{}{
			fb.ID, fb.ProjectID, fb.Title, fb.Description, fb.CustomData, fb.FilePaths,
			fb.Status, fb.Tags, fb.Assignee, fb.ContactName, fb.ContactEmail, clientIP,
			fb.CreatedAt.Format("2006-01-02 15:04:05"),
		}
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		f.SetSheetRow(sheet, cell, &row)
	}

	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	if err := f.Write(c.Writer); err != nil {
		log.Printf("[EXPORT] XLSX write failed: %v", err)
	}
}

// ========== Chart Data ==========

func (a *App) AdminChartData(c *gin.Context) {
	daysStr := c.DefaultQuery("days", "30")
	days, _ := strconv.Atoi(daysStr)
	if days <= 0 || days > 365 {
		days = 30
	}

	trend, err := a.DB.GetDailyTrend(days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取趋势数据失败"})
		return
	}
	if trend == nil {
		trend = []map[string]interface{}{}
	}

	statusDist, err := a.DB.GetStatusDistribution()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取状态分布失败"})
		return
	}
	if statusDist == nil {
		statusDist = []map[string]interface{}{}
	}

	catDist, err := a.DB.GetCategoryCounts("")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取分类分布失败"})
		return
	}
	if catDist == nil {
		catDist = map[string]int{}
	}

	c.JSON(http.StatusOK, gin.H{
		"daily_trend":           trend,
		"status_distribution":   statusDist,
		"category_distribution": catDist,
	})
}

// ========== Audit Logs ==========

func (a *App) AdminListAuditLogs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	logs, total, err := a.DB.ListAuditLogs(limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询审计日志失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"logs":  logs,
		"total": total,
	})
}

// ========== Backup ==========

func (a *App) AdminBackup(c *gin.Context) {
	backupDir := filepath.Join(a.Cfg.DataDir, "backups")
	backupPath, err := a.DB.BackupDatabase(backupDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "备份失败: " + err.Error()})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("backup", fmt.Sprintf("数据库备份: %s", filepath.Base(backupPath)), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{
		"message": "备份完成",
		"path":    filepath.Base(backupPath),
	})
}

// ========== CSV Import ==========

// AdminImportCSV imports feedbacks from a CSV file.
func (a *App) AdminImportCSV(c *gin.Context) {
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请上传 CSV 文件"})
		return
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CSV 解析失败: " + err.Error()})
		return
	}

	if len(records) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CSV 文件为空或只有表头"})
		return
	}

	// Chinese → English header alias map
	cnToEn := map[string]string{
		"标题":    "title",
		"描述":    "description",
		"状态":    "status",
		"标签":    "tags",
		"指派":    "assignee",
		"联系人":   "contact_name",
		"联系邮箱":  "contact_email",
		"优先级":   "priority",
		"提交时间":  "created_at",
		"项目":    "project_id",
		"自定义字段": "custom_data",
		"附件":    "file_paths",
		"来源ip":  "client_ip",
	}

	// Parse header to find column indices (normalized to English names)
	header := records[0]
	colIndex := map[string]int{}
	for i, h := range header {
		normalized := strings.TrimSpace(strings.ToLower(h))
		if en, ok := cnToEn[normalized]; ok {
			normalized = en
		}
		colIndex[normalized] = i
	}

	if _, ok := colIndex["title"]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CSV 缺少必要列: title (标题)"})
		return
	}

	// Validate project_id from form or CSV column
	globalProjectID := c.PostForm("project_id")

	// If no form project_id, check if CSV has a project_id column and validate first row
	if globalProjectID == "" {
		if _, hasProjCol := colIndex["project_id"]; !hasProjCol {
			// No project specified anywhere — validate "default" exists
			proj, projErr := a.DB.GetProjectBySlug("default")
			if projErr != nil || proj == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "未指定项目且 default 项目不存在，请通过表单 project_id 或 CSV 项目列指定"})
				return
			}
			if !proj.IsActive {
				c.JSON(http.StatusBadRequest, gin.H{"error": "default 项目已停用"})
				return
			}
			// Bug #7: Check write permission on the default project
			if !a.checkProjectWritePerm(c, "default") {
				c.JSON(http.StatusForbidden, gin.H{"error": "您没有 default 项目的编辑权限"})
				return
			}
			globalProjectID = "default"
		}
		// else: per-row project_id will be used
	} else {
		// Validate form-specified project exists
		proj, projErr := a.DB.GetProjectBySlug(globalProjectID)
		if projErr != nil || proj == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "项目不存在: " + globalProjectID})
			return
		}
		if !proj.IsActive {
			c.JSON(http.StatusBadRequest, gin.H{"error": "项目已停用: " + globalProjectID})
			return
		}
		// Bug #7: Check write permission on the form-specified project
		if !a.checkProjectWritePerm(c, globalProjectID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "您没有该项目的编辑权限"})
			return
		}
	}

	// parseCreatedAt tries multiple formats: unix timestamp, "2006-01-02 15:04:05", "2006-01-02"
	parseCreatedAt := func(s string) int64 {
		s = strings.TrimSpace(s)
		if s == "" {
			return 0
		}
		// Try unix timestamp
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			return v
		}
		// Try common datetime formats
		for _, layout := range []string{
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05",
			"2006-01-02T15:04:05Z",
			"2006-01-02",
			"2006/01/02 15:04:05",
			"2006/01/02",
		} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.Unix()
			}
		}
		return 0
	}

	imported := 0
	errors := []string{}
	for i, row := range records[1:] {
		lineNum := i + 2
		getCol := func(name string) string {
			if idx, ok := colIndex[name]; ok && idx < len(row) {
				return strings.TrimSpace(row[idx])
			}
			return ""
		}

		title := getCol("title")
		if title == "" {
			errors = append(errors, fmt.Sprintf("第 %d 行: 标题为空，已跳过", lineNum))
			continue
		}

		// Determine project_id: per-row > form > default
		pid := globalProjectID
		if rowProj := getCol("project_id"); rowProj != "" && globalProjectID == "" {
			// Validate per-row project
			proj, projErr := a.DB.GetProjectBySlug(rowProj)
			if projErr != nil || proj == nil {
				errors = append(errors, fmt.Sprintf("第 %d 行: 项目不存在: %s", lineNum, rowProj))
				continue
			}
			if !proj.IsActive {
				errors = append(errors, fmt.Sprintf("第 %d 行: 项目已停用: %s", lineNum, rowProj))
				continue
			}
			// Bug #7: Check write permission on per-row project
			if !a.checkProjectWritePerm(c, rowProj) {
				errors = append(errors, fmt.Sprintf("第 %d 行: 您没有该项目的编辑权限", lineNum))
				continue
			}
			pid = rowProj
		}

		createdAtUnix := parseCreatedAt(getCol("created_at"))

		// Bug #8: Validate status and priority from CSV
		rawStatus := getCol("status")
		if rawStatus != "" && !database.ValidStatuses[rawStatus] {
			errors = append(errors, fmt.Sprintf("第 %d 行: 无效的状态值 %q，已跳过", lineNum, rawStatus))
			continue
		}
		validPriorities := map[string]bool{"": true, "low": true, "medium": true, "high": true, "urgent": true}
		rawPriority := getCol("priority")
		if !validPriorities[rawPriority] {
			errors = append(errors, fmt.Sprintf("第 %d 行: 无效的优先级 %q，已跳过", lineNum, rawPriority))
			continue
		}

		// Generate tracking token for submitter self-service
		tokenBytes := make([]byte, 16)
		rand.Read(tokenBytes)
		trackingToken := hex.EncodeToString(tokenBytes)

		fb := &database.Feedback{
			ProjectID:     pid,
			Title:         title,
			Description:   getCol("description"),
			Status:        getCol("status"),
			Tags:          getCol("tags"),
			Assignee:      getCol("assignee"),
			ContactName:   getCol("contact_name"),
			ContactEmail:  getCol("contact_email"),
			Priority:      getCol("priority"),
			CustomData:    getCol("custom_data"),
			ClientIP:      "csv-import",
			TrackingToken: trackingToken,
		}

		if _, err := a.DB.ImportFeedback(fb, createdAtUnix); err != nil {
			errors = append(errors, fmt.Sprintf("第 %d 行: 导入失败: %v", lineNum, err))
			continue
		}
		imported++
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("csv_import", fmt.Sprintf("CSV 导入 %d 条反馈", imported), fmt.Sprintf("%v", user), clientIP)

	result := gin.H{
		"imported": imported,
		"total":    len(records) - 1,
	}
	if len(errors) > 0 {
		result["errors"] = errors
	}
	c.JSON(http.StatusOK, result)
}

// ========== Data Archive & Cleanup ==========

// AdminArchiveOldFeedbacks archives old pending/processing feedbacks.
func (a *App) AdminArchiveOldFeedbacks(c *gin.Context) {
	var req struct {
		DaysOld int `json:"days_old"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.DaysOld <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请指定有效天数"})
		return
	}

	affected, err := a.DB.ArchiveOldFeedbacks(req.DaysOld)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "归档失败"})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("archive", fmt.Sprintf("归档 %d 条超过 %d 天的反馈", affected, req.DaysOld), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{
		"message":  fmt.Sprintf("已归档 %d 条反馈", affected),
		"archived": affected,
	})
}

// AdminPruneOldBackups removes old backup files.
func (a *App) AdminPruneOldBackups(c *gin.Context) {
	var req struct {
		DaysOld int `json:"days_old"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.DaysOld <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请指定有效天数"})
		return
	}

	backupDir := filepath.Join(a.Cfg.DataDir, "backups")
	pruned, err := a.DB.PruneOldBackups(backupDir, req.DaysOld)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "清理失败: " + err.Error()})
		return
	}

	user, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("prune_backups", fmt.Sprintf("清理 %d 个超过 %d 天的备份", pruned, req.DaysOld), fmt.Sprintf("%v", user), clientIP)

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("已清理 %d 个备份文件", pruned),
		"pruned":  pruned,
	})
}
