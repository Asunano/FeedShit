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
	err = d.db.QueryRow(`SELECT COUNT(DISTINCT project_id) FROM feedbacks`).Scan(&projects)
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
	err = d.db.QueryRow(`SELECT COUNT(DISTINCT project_id) FROM feedbacks WHERE `+inClause, args...).Scan(&projects)
	if err != nil {
		return
	}
	todayStr := time.Now().Format("2006-01-02")
	err = d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks WHERE `+inClause+` AND date(created_at, 'unixepoch') = ?`, append(args, todayStr)...).Scan(&today)
	return
}

// GetDailyTrend returns feedback counts per day for the last N days.
func (d *Database) GetDailyTrend(days int) ([]map[string]interface{}, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	cutoff := time.Now().AddDate(0, 0, -days).Unix()

	rows, err := d.db.Query(`
		SELECT date(created_at, 'unixepoch') as day, COUNT(*) as cnt
		FROM feedbacks
		WHERE created_at >= ?
		GROUP BY day ORDER BY day ASC
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var day string
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"date":  day,
			"count": count,
		})
	}
	return result, nil
}

// GetDailyTrendInRange 返回指定时间范围内每日反馈数。
func (d *Database) GetDailyTrendInRange(startUnix, endUnix int64) ([]map[string]interface{}, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT date(created_at, 'unixepoch') as day, COUNT(*) as cnt
		FROM feedbacks
		WHERE created_at >= ? AND created_at <= ?
		GROUP BY day ORDER BY day ASC
	`, startUnix, endUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var day string
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"date":  day,
			"count": count,
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
