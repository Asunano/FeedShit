package database

import (
	"database/sql"
	"strings"
	"time"
)

// GetStats returns dashboard statistics.
func (d *Database) GetStats() (total int, projects int, today int, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	err = d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks`).Scan(&total)
	if err != nil {
		return
	}
	err = d.db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&projects)
	if err != nil {
		return
	}
	todayStr := time.Now().Format("2006-01-02")
	err = d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks WHERE date(created_at, 'unixepoch') = ?`, todayStr).Scan(&today)
	return
}

// GetStatsForProjects returns stats scoped to a list of project slugs.
// Used for non-admin users with limited member_grants.
func (d *Database) GetStatsForProjects(projectIDs []string) (total int, projects int, today int, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if len(projectIDs) == 0 {
		return 0, 0, 0, nil
	}

	placeholders := make([]string, len(projectIDs))
	args := make([]interface{}, len(projectIDs))
	for i, pid := range projectIDs {
		placeholders[i] = "?"
		args[i] = pid
	}
	inClause := "project_id IN (" + strings.Join(placeholders, ",") + ")"

	err = d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks WHERE `+inClause, args...).Scan(&total)
	if err != nil {
		return
	}
	// Scoped stats: "all projects" for this admin = the projects they manage.
	projects = len(projectIDs)
	todayStr := time.Now().Format("2006-01-02")
	err = d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks WHERE `+inClause+` AND date(created_at, 'unixepoch') = ?`, append(args, todayStr)...).Scan(&today)
	return
}

// CountPendingFeedbacks returns how many feedbacks are awaiting first review
// (status = 'pending'). When projectIDs is non-nil the count is scoped to those
// projects; nil means all projects (admin view).
func (d *Database) CountPendingFeedbacks(projectIDs []string) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	where := `WHERE status = 'pending'`
	args := []interface{}{}
	if len(projectIDs) > 0 {
		placeholders := make([]string, len(projectIDs))
		for i, pid := range projectIDs {
			placeholders[i] = "?"
			args = append(args, pid)
		}
		where += " AND project_id IN (" + strings.Join(placeholders, ",") + ")"
	}
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks `+where, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// GetDailyTrend returns feedback counts per day for the last N days.
// Every day in the window is pre-filled (zero where there is no data) so the
// chart never stretches a single early point across the entire width.
func (d *Database) GetDailyTrend(days int) ([]map[string]interface{}, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if days <= 0 {
		days = 30
	}
	cutoff := time.Now().AddDate(0, 0, -(days - 1)).Unix()

	rows, err := d.db.Query(`
		SELECT date(created_at, 'unixepoch') as day, COUNT(*) as cnt
		FROM feedbacks
		WHERE created_at >= ?
		GROUP BY day`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var day string
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		counts[day] = count
	}

	now := time.Now()
	result := make([]map[string]interface{}, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		result = append(result, map[string]interface{}{
			"date":  day,
			"count": counts[day],
		})
	}
	return result, nil
}

// GetDailyTrendInRange 返回指定时间范围内每日反馈数（含无数据的日期，预填为 0）。
func (d *Database) GetDailyTrendInRange(startUnix, endUnix int64) ([]map[string]interface{}, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT date(created_at, 'unixepoch') as day, COUNT(*) as cnt
		FROM feedbacks
		WHERE created_at >= ? AND created_at <= ?
		GROUP BY day`, startUnix, endUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var day string
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		counts[day] = count
	}

	start := time.Unix(startUnix, 0)
	end := time.Unix(endUnix, 0)
	result := []map[string]interface{}{}
	for t := start; !t.After(end); t = t.AddDate(0, 0, 1) {
		day := t.Format("2006-01-02")
		result = append(result, map[string]interface{}{
			"date":  day,
			"count": counts[day],
		})
	}
	return result, nil
}

// GetStatusDistribution returns feedback counts grouped by status.
func (d *Database) GetStatusDistribution() ([]map[string]interface{}, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT status, COUNT(*) as cnt FROM feedbacks GROUP BY status ORDER BY cnt DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"status": status,
			"count":  count,
		})
	}
	return result, nil
}

// GetWeeklyStatusDistribution 返回指定时间范围内各状态的反馈数。
func (d *Database) GetWeeklyStatusDistribution(startUnix, endUnix int64) (map[string]int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT status, COUNT(*) as cnt
		FROM feedbacks
		WHERE created_at >= ? AND created_at <= ?
		GROUP BY status ORDER BY cnt DESC
	`, startUnix, endUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		result[status] = count
	}
	return result, nil
}

// GetAvgResolutionSeconds returns the average seconds from feedback creation to
// its first transition into 'resolved' or 'closed', scoped to projectIDs (nil = all).
// The count of resolved feedbacks is returned alongside the average so the UI can
// show "基于 N 条已解决反馈".
func (d *Database) GetAvgResolutionSeconds(projectIDs []string) (float64, int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	where := ""
	args := []interface{}{}
	if len(projectIDs) > 0 {
		placeholders := make([]string, len(projectIDs))
		for i, pid := range projectIDs {
			placeholders[i] = "?"
			args = append(args, pid)
		}
		where = " AND f.project_id IN (" + strings.Join(placeholders, ",") + ")"
	}

	var avgSec float64
	var count int
	err := d.db.QueryRow(`
		SELECT COALESCE(AVG(sh.created_at - f.created_at), 0), COUNT(*)
		FROM feedbacks f
		JOIN feedback_status_history sh ON sh.feedback_id = f.id
		WHERE sh.to_status IN ('resolved','closed')
		  AND sh.created_at = (
		    SELECT MIN(created_at) FROM feedback_status_history
		    WHERE feedback_id = f.id AND to_status IN ('resolved','closed')
		  )`+where,
		args...,
	).Scan(&avgSec, &count)
	if err != nil {
		return 0, 0, err
	}
	return avgSec, count, nil
}

// GetWeeklyStats 返回指定时间范围内的反馈总数和涉及项目数。
func (d *Database) GetWeeklyStats(startUnix, endUnix int64) (total int, projects int, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	err = d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks WHERE created_at >= ? AND created_at <= ?`, startUnix, endUnix).Scan(&total)
	if err != nil {
		return
	}
	err = d.db.QueryRow(`SELECT COUNT(DISTINCT project_id) FROM feedbacks WHERE created_at >= ? AND created_at <= ?`, startUnix, endUnix).Scan(&projects)
	return
}

// GetWeeklyCategoryCounts 返回指定时间范围内各分类的反馈数。
func (d *Database) GetWeeklyCategoryCounts(startUnix, endUnix int64) (map[string]int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT category, COUNT(*) as cnt
		FROM feedbacks
		WHERE created_at >= ? AND created_at <= ?
		GROUP BY category ORDER BY cnt DESC
	`, startUnix, endUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var cat string
		var count int
		if err := rows.Scan(&cat, &count); err != nil {
			return nil, err
		}
		result[cat] = count
	}
	return result, nil
}

// GetWeeklyProjectStats 返回指定时间范围内各项目的反馈统计。
func (d *Database) GetWeeklyProjectStats(startUnix, endUnix int64) ([]map[string]interface{}, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT f.project_id, COUNT(*) as cnt,
			   COALESCE(MAX(f.created_at), 0) as latest,
			   COALESCE(p.name, '') as project_name
		FROM feedbacks f
		LEFT JOIN projects p ON p.slug = f.project_id
		WHERE f.created_at >= ? AND f.created_at <= ?
		GROUP BY f.project_id
		ORDER BY cnt DESC
	`, startUnix, endUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var projectID string
		var projectName sql.NullString
		var count int
		var latestAt int64
		if err := rows.Scan(&projectID, &count, &latestAt, &projectName); err != nil {
			return nil, err
		}
		name := ""
		if projectName.Valid {
			name = projectName.String
		}
		latest := ""
		if latestAt > 0 {
			latest = time.Unix(latestAt, 0).Format("2006-01-02 15:04:05")
		}
		result = append(result, map[string]interface{}{
			"project_id":   projectID,
			"project_name": name,
			"count":        count,
			"latest_at":    latest,
		})
	}
	return result, nil
}
