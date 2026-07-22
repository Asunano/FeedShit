package database

import (
	"testing"
)

// TestListFeedbacksSort 验证全局排序（priority / votes / 默认时间）按数据库 ORDER BY 生效（管理后台 #2）。
func TestListFeedbacksSort(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	// 插入不同优先级的反馈
	idLow, _ := db.InsertFeedback(&Feedback{ProjectID: "p1", Title: "low", Priority: "low"})
	idUrgent, _ := db.InsertFeedback(&Feedback{ProjectID: "p1", Title: "urgent", Priority: "urgent"})
	idHigh, _ := db.InsertFeedback(&Feedback{ProjectID: "p1", Title: "high", Priority: "high"})

	// 投票：high 得 2 票，low 得 1 票，urgent 得 0 票
	if _, err := db.db.Exec(`INSERT INTO feedback_votes(feedback_id, voter_key) VALUES (?, ?)`, idHigh, "voterA"); err != nil {
		t.Fatalf("vote high A: %v", err)
	}
	if _, err := db.db.Exec(`INSERT INTO feedback_votes(feedback_id, voter_key) VALUES (?, ?)`, idHigh, "voterB"); err != nil {
		t.Fatalf("vote high B: %v", err)
	}
	if _, err := db.db.Exec(`INSERT INTO feedback_votes(feedback_id, voter_key) VALUES (?, ?)`, idLow, "voterC"); err != nil {
		t.Fatalf("vote low: %v", err)
	}

	// 默认（created_at DESC）：三者在同一次测试内创建时间相同，仅校验全部返回
	def, _, err := db.ListFeedbacks(nil, nil, "", 50, 0)
	if err != nil {
		t.Fatalf("default sort: %v", err)
	}
	if len(def) != 3 {
		t.Fatalf("default count = %d, want 3", len(def))
	}

	// priority：urgent > high > low
	byPri, _, err := db.ListFeedbacks(nil, nil, "priority", 50, 0)
	if err != nil {
		t.Fatalf("priority sort: %v", err)
	}
	if len(byPri) != 3 || byPri[0].ID != idUrgent || byPri[1].ID != idHigh || byPri[2].ID != idLow {
		t.Fatalf("priority order = %v, want [urgent, high, low]", idsOf(byPri))
	}

	// votes：high(2) > low(1) > urgent(0)
	byVotes, _, err := db.ListFeedbacks(nil, nil, "votes", 50, 0)
	if err != nil {
		t.Fatalf("votes sort: %v", err)
	}
	if len(byVotes) != 3 || byVotes[0].ID != idHigh || byVotes[1].ID != idLow || byVotes[2].ID != idUrgent {
		t.Fatalf("votes order = %v, want [high, low, urgent]", idsOf(byVotes))
	}
}

func idsOf(list []Feedback) []int64 {
	out := make([]int64, len(list))
	for i, f := range list {
		out[i] = f.ID
	}
	return out
}
