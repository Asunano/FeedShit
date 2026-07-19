package database

import (
	"strings"
	"testing"
	"time"

	"feedshit/internal/config"
)

// TestMigrateCreatesJobLocksTable 验证 migrate 后 job_locks 表存在且结构匹配（第 1 项）。
func TestMigrateCreatesJobLocksTable(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	// 验证表可以通过 PRAGMA table_info 查询
	rows, err := db.db.Query(`PRAGMA table_info(job_locks)`)
	if err != nil {
		t.Fatalf("job_locks 表不存在: %v", err)
	}
	defer rows.Close()

	cols := make(map[string]string)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("读取列信息失败: %v", err)
		}
		cols[name] = colType
	}

	expected := map[string]string{"key": "TEXT", "token": "TEXT", "locked_until": "INTEGER"}
	for name, typ := range expected {
		if got, ok := cols[name]; !ok {
			t.Fatalf("缺少列 %s", name)
		} else if got != typ {
			t.Fatalf("列 %s 类型 = %s, 期望 %s", name, got, typ)
		}
	}
}

// TestMigrateJobLocksIdempotent 验证重复 migrate 幂等（第 1 项）。
func TestMigrateJobLocksIdempotent(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	// 第二次 migrate 不应报错
	if err := db.migrate(); err != nil {
		t.Fatalf("重复 migrate 应幂等，got: %v", err)
	}

	// 验证 job_locks 表仍可用
	_, err = db.db.Exec(`INSERT INTO job_locks (key, token, locked_until) VALUES (?, ?, ?)`, "k", "t", 1)
	if err != nil {
		t.Fatalf("migrate 后插入 job_locks 失败: %v", err)
	}
}

// TestInitDefaultConfigIncludesReportRecipients 验证 InitDefaultConfig 含 report_recipients（第 4 项）。
func TestInitDefaultConfigIncludesReportRecipients(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	// InitDefaultConfig 需要 config.Config，用空配置即可
	cfg := &config.Config{}
	db.InitDefaultConfig(cfg)

	val := db.GetConfig("report_recipients")
	if val != "" {
		t.Fatalf("report_recipients 默认值应为空字符串, got %q", val)
	}

	// 验证 config 表中存在该条目
	var count int
	db.db.QueryRow(`SELECT COUNT(*) FROM config WHERE key = 'report_recipients'`).Scan(&count)
	if count == 0 {
		t.Fatal("config 表中缺少 report_recipients 条目")
	}
}

// TestExecRawWorks 验证 ExecRaw 可用（第 4 项）。
func TestExecRawWorks(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	// 使用 ExecRaw 插入
	res, err := db.ExecRaw(`INSERT INTO job_locks (key, token, locked_until) VALUES (?, ?, ?)`, "raw_test", "token123", 42)
	if err != nil {
		t.Fatalf("ExecRaw 插入失败: %v", err)
	}
	rows, _ := res.RowsAffected()
	if rows != 1 {
		t.Fatalf("ExecRaw 影响行数 = %d, 期望 1", rows)
	}

	// 使用 QueryRaw 验证
	qrows, err := db.QueryRaw(`SELECT token, locked_until FROM job_locks WHERE key = ?`, "raw_test")
	if err != nil {
		t.Fatalf("QueryRaw 查询失败: %v", err)
	}
	defer qrows.Close()
	if !qrows.Next() {
		t.Fatal("QueryRaw 未返回行")
	}
	var token string
	var lu int
	if err := qrows.Scan(&token, &lu); err != nil {
		t.Fatalf("QueryRaw Scan 失败: %v", err)
	}
	if token != "token123" || lu != 42 {
		t.Fatalf("QueryRaw 结果 = (%q, %d), 期望 ('token123', 42)", token, lu)
	}
}

// TestGetWeeklyStats 验证周级统计返回正确过滤结果（第 6 项）。
func TestGetWeeklyStats(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	now := time.Now().Unix()
	// 插入 3 条 "上周" 数据（过去 10 天以内）
	old1 := now - 5*86400
	old2 := now - 6*86400
	old3 := now - 7*86400
	// 插入 2 条 "更旧" 数据（应被过滤掉）
	older1 := now - 20*86400
	older2 := now - 21*86400

	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "proj-a", "old1", "pending", old1)
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "proj-a", "old2", "processing", old2)
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "proj-b", "old3", "resolved", old3)
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "proj-c", "older1", "closed", older1)
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "proj-a", "older2", "closed", older2)

	startUnix := now - 10*86400
	endUnix := now

	total, projects, err := db.GetWeeklyStats(startUnix, endUnix)
	if err != nil {
		t.Fatalf("GetWeeklyStats 失败: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, 期望 3（仅统计过去 10 天内的 3 条）", total)
	}
	if projects != 2 {
		t.Fatalf("projects = %d, 期望 2（proj-a, proj-b）", projects)
	}
}

// TestGetWeeklyCategoryCounts 验证分类统计过滤（第 6 项）。
func TestGetWeeklyCategoryCounts(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	now := time.Now().Unix()
	inRange := now - 3*86400
	outOfRange := now - 30*86400

	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, category, created_at) VALUES (?, ?, ?, ?, ?)`, "proj", "f1", "pending", "bug", inRange)
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, category, created_at) VALUES (?, ?, ?, ?, ?)`, "proj", "f2", "pending", "feature", inRange)
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, category, created_at) VALUES (?, ?, ?, ?, ?)`, "proj", "f3", "pending", "bug", inRange)
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, category, created_at) VALUES (?, ?, ?, ?, ?)`, "proj", "old", "pending", "legacy", outOfRange)

	result, err := db.GetWeeklyCategoryCounts(now-10*86400, now)
	if err != nil {
		t.Fatalf("GetWeeklyCategoryCounts 失败: %v", err)
	}
	if result["bug"] != 2 {
		t.Fatalf("bug count = %d, 期望 2", result["bug"])
	}
	if result["feature"] != 1 {
		t.Fatalf("feature count = %d, 期望 1", result["feature"])
	}
	if _, ok := result["legacy"]; ok {
		t.Fatal("legacy 不应出现在结果中（超出时间范围）")
	}
}

// TestGetWeeklyStatusDistribution 验证状态分布过滤（第 6 项）。
func TestGetWeeklyStatusDistribution(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	now := time.Now().Unix()
	inRange := now - 2*86400

	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "p", "f1", "pending", inRange)
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "p", "f2", "pending", inRange)
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "p", "f3", "resolved", inRange)

	result, err := db.GetWeeklyStatusDistribution(now-10*86400, now)
	if err != nil {
		t.Fatalf("GetWeeklyStatusDistribution 失败: %v", err)
	}
	if result["pending"] != 2 {
		t.Fatalf("pending count = %d, 期望 2", result["pending"])
	}
	if result["resolved"] != 1 {
		t.Fatalf("resolved count = %d, 期望 1", result["resolved"])
	}
}

// TestGetDailyTrendInRange 验证每日趋势过滤（第 6 项）。
func TestGetDailyTrendInRange(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	twoDaysAgoStart := todayStart.AddDate(0, 0, -2)
	lastWeekStart := todayStart.AddDate(0, 0, -10)

	// 插入 3 天在范围内、1 天范围外
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "p", "today1", "pending", todayStart.Unix())
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "p", "today2", "pending", todayStart.Unix()+3600)
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "p", "yest", "pending", yesterdayStart.Unix())
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "p", "twoDays", "pending", twoDaysAgoStart.Unix())
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "p", "old", "pending", lastWeekStart.Unix())

	result, err := db.GetDailyTrendInRange(twoDaysAgoStart.Unix(), todayStart.Unix()+86400)
	if err != nil {
		t.Fatalf("GetDailyTrendInRange 失败: %v", err)
	}

	// 应有 3 天数据（前天、昨天、今天）
	if len(result) != 3 {
		t.Fatalf("天数 = %d, 期望 3", len(result))
	}

	// 验证每天计数：使用兼容 int/int64 的取值方式
	for _, d := range result {
		day := d["date"].(string)
		var count int
		if c, ok := d["count"].(int); ok {
			count = c
		} else if c, ok := d["count"].(int64); ok {
			count = int(c)
		} else {
			t.Fatalf("count 字段类型异常: %T", d["count"])
		}
		if strings.HasPrefix(day, todayStart.Format("2006-01-02")) {
			if count != 2 {
				t.Fatalf("今日 count = %d, 期望 2", count)
			}
		}
	}
}

// TestGetWeeklyProjectStats 验证项目统计过滤（第 6 项）。
func TestGetWeeklyProjectStats(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	now := time.Now().Unix()
	inRange := now - 3*86400
	outOfRange := now - 30*86400

	// 插入项目
	db.db.Exec(`INSERT INTO projects (slug, name) VALUES (?, ?)`, "proj-a", "Project A")
	db.db.Exec(`INSERT INTO projects (slug, name) VALUES (?, ?)`, "proj-b", "Project B")

	// 插入范围内的反馈
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "proj-a", "f1", "pending", inRange)
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "proj-a", "f2", "pending", inRange)
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "proj-b", "f3", "pending", inRange)
	// 范围外
	db.db.Exec(`INSERT INTO feedbacks (project_id, title, status, created_at) VALUES (?, ?, ?, ?)`, "proj-b", "old", "pending", outOfRange)

	result, err := db.GetWeeklyProjectStats(now-10*86400, now)
	if err != nil {
		t.Fatalf("GetWeeklyProjectStats 失败: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("项目数 = %d, 期望 2", len(result))
	}

	for _, p := range result {
		pid := p["project_id"].(string)

		var count int
		if c, ok := p["count"].(int); ok {
			count = c
		} else if c, ok := p["count"].(int64); ok {
			count = int(c)
		} else {
			t.Fatalf("count 字段类型异常: %T", p["count"])
		}

		if pid == "proj-a" && count != 2 {
			t.Fatalf("proj-a count = %d, 期望 2", count)
		}
		if pid == "proj-b" && count != 1 {
			t.Fatalf("proj-b count = %d, 期望 1", count)
		}
		// 验证项目名从 JOIN 获取
		name := p["project_name"].(string)
		if pid == "proj-a" && name != "Project A" {
			t.Fatalf("proj-a 项目名 = %q, 期望 'Project A'", name)
		}
	}
}
