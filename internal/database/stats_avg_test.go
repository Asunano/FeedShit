package database

import (
	"testing"
)

// TestGetAvgResolutionSeconds 验证按"创建→首次解决"计算平均处理时长（管理后台 #3）。
func TestGetAvgResolutionSeconds(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	t0 := int64(1_700_000_000)

	// 反馈 A：创建于 t0，1 小时后解决（3600s）
	idA, err := db.InsertFeedback(&Feedback{ProjectID: "p1", Title: "A", Priority: "low"})
	if err != nil {
		t.Fatalf("insert A failed: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE feedbacks SET created_at = ? WHERE id = ?`, t0, idA); err != nil {
		t.Fatalf("set created_at A: %v", err)
	}
	if err := db.RecordStatusChange(idA, "pending", "resolved", "tester", ""); err != nil {
		t.Fatalf("record resolved A: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE feedback_status_history SET created_at = ? WHERE feedback_id = ? AND to_status = 'resolved'`, t0+3600, idA); err != nil {
		t.Fatalf("set hist A: %v", err)
	}

	// 反馈 B：创建于 t0，2 小时后关闭（7200s）
	idB, err := db.InsertFeedback(&Feedback{ProjectID: "p1", Title: "B", Priority: "medium"})
	if err != nil {
		t.Fatalf("insert B failed: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE feedbacks SET created_at = ? WHERE id = ?`, t0, idB); err != nil {
		t.Fatalf("set created_at B: %v", err)
	}
	if err := db.RecordStatusChange(idB, "pending", "closed", "tester", ""); err != nil {
		t.Fatalf("record closed B: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE feedback_status_history SET created_at = ? WHERE feedback_id = ? AND to_status = 'closed'`, t0+7200, idB); err != nil {
		t.Fatalf("set hist B: %v", err)
	}

	// 反馈 C：未解决，不应计入
	if _, err := db.InsertFeedback(&Feedback{ProjectID: "p1", Title: "C", Priority: "high"}); err != nil {
		t.Fatalf("insert C failed: %v", err)
	}

	want := float64(3600+7200) / 2 // 5400

	avgSec, count, err := db.GetAvgResolutionSeconds(nil)
	if err != nil {
		t.Fatalf("GetAvgResolutionSeconds failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("resolved count = %d, want 2", count)
	}
	if avgSec < want-1 || avgSec > want+1 {
		t.Fatalf("avgSec = %v, want ~%v", avgSec, want)
	}

	// 作用域：只统计 p1（全部都是 p1，结果一致）
	scoped, sc, err := db.GetAvgResolutionSeconds([]string{"p1"})
	if err != nil {
		t.Fatalf("scoped failed: %v", err)
	}
	if sc != 2 || scoped < want-1 || scoped > want+1 {
		t.Fatalf("scoped avg = %v (count %d), want ~%v (count 2)", scoped, sc, want)
	}

	// 空作用域返回 0
	empty, ec, err := db.GetAvgResolutionSeconds([]string{"nope"})
	if err != nil {
		t.Fatalf("empty failed: %v", err)
	}
	if empty != 0 || ec != 0 {
		t.Fatalf("empty avg = %v (count %d), want 0/0", empty, ec)
	}
}
