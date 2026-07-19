package report

import (
	"database/sql"
	"testing"
	"time"

	"feedshit/internal/database"
)

// setupJobLockDB creates a fresh in-memory DB for job lock tests.
func setupJobLockDB(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	return db
}

// readLockedUntil reads the locked_until value for a given lock key.
func readLockedUntil(t *testing.T, db *database.Database, key string) int64 {
	t.Helper()
	rows, err := db.QueryRaw(`SELECT locked_until FROM job_locks WHERE key = ?`, key)
	if err != nil {
		t.Fatalf("failed to query locked_until for key=%s: %v", key, err)
	}
	defer rows.Close()
	var lockedUntil int64
	if rows.Next() {
		if err := rows.Scan(&lockedUntil); err != nil {
			t.Fatalf("failed to scan locked_until for key=%s: %v", key, err)
		}
		return lockedUntil
	}
	return -1 // not found
}

// TestAcquireJobLock_FirstCallSuccess 验证首次调用成功（第 2 项）。
func TestAcquireJobLock_FirstCallSuccess(t *testing.T) {
	db := setupJobLockDB(t)
	_, ok := AcquireJobLock(db, "test_lock_1", 1*time.Hour)
	if !ok {
		t.Fatal("首次调用 AcquireJobLock 应成功")
	}
	lockedUntil := readLockedUntil(t, db, "test_lock_1")
	if lockedUntil <= 0 {
		t.Fatalf("expected locked_until>0, got %d", lockedUntil)
	}
}

// TestAcquireJobLock_SecondInstanceFails 验证另一实例抢锁失败（第 2 项）。
func TestAcquireJobLock_SecondInstanceFails(t *testing.T) {
	db := setupJobLockDB(t)

	// 模拟第一个实例已持有锁
	_, err := db.ExecRaw(
		`INSERT INTO job_locks (key, token, locked_until) VALUES (?, ?, ?)`,
		"test_lock_2", "other-instance-token", time.Now().Unix()+int64(3600),
	)
	if err != nil {
		t.Fatalf("插入模拟锁记录失败: %v", err)
	}

	_, ok := AcquireJobLock(db, "test_lock_2", 1*time.Hour)
	if ok {
		t.Fatal("另一实例应无法获取已被持有的锁")
	}
}

// TestAcquireJobLock_ExpiredLockReacquire 验证过期锁可被新实例抢走（第 2 项）。
func TestAcquireJobLock_ExpiredLockReacquire(t *testing.T) {
	db := setupJobLockDB(t)

	// 插入一个已过期的锁
	_, err := db.ExecRaw(
		`INSERT INTO job_locks (key, token, locked_until) VALUES (?, ?, ?)`,
		"test_lock_3", "expired-token", time.Now().Unix()-1,
	)
	if err != nil {
		t.Fatalf("插入过期锁记录失败: %v", err)
	}

	_, ok := AcquireJobLock(db, "test_lock_3", 1*time.Hour)
	if !ok {
		t.Fatal("过期锁应可被新实例抢走")
	}
}

// TestReleaseJobLock 验证释放锁后 locked_until = 0（第 3 项）。
func TestReleaseJobLock(t *testing.T) {
	db := setupJobLockDB(t)

	token, ok := AcquireJobLock(db, "test_lock_5", 1*time.Hour)
	if !ok {
		t.Fatal("首次获取锁应成功")
	}

	ReleaseJobLock(db, "test_lock_5", token)
	lockedUntil := readLockedUntil(t, db, "test_lock_5")
	if lockedUntil != 0 {
		t.Fatalf("释放后 locked_until 应为 0，got %d", lockedUntil)
	}
}

// TestReleaseJobLock_ThenReacquire 验证释放后可重新抢锁（第 3 项）。
func TestReleaseJobLock_ThenReacquire(t *testing.T) {
	db := setupJobLockDB(t)

	token, _ := AcquireJobLock(db, "test_lock_6", 1*time.Hour)
	ReleaseJobLock(db, "test_lock_6", token)

	lockedUntil := readLockedUntil(t, db, "test_lock_6")
	if lockedUntil != 0 {
		t.Fatalf("释放后 locked_until 应为 0，got %d", lockedUntil)
	}

	_, ok := AcquireJobLock(db, "test_lock_6", 1*time.Hour)
	if !ok {
		t.Fatal("释放后应可重新获取锁")
	}
	lockedUntil = readLockedUntil(t, db, "test_lock_6")
	if lockedUntil <= 0 {
		t.Fatalf("重新获取后 locked_until 应 > 0，got %d", lockedUntil)
	}
}

// TestJobLockTableExists 验证 migrate 后 job_locks 表存在且结构匹配（第 1 项）。
func TestJobLockTableExists(t *testing.T) {
	db := setupJobLockDB(t)

	rows, err := db.QueryRaw(`PRAGMA table_info(job_locks)`)
	if err != nil {
		t.Fatalf("job_locks 表不存在: %v", err)
	}
	defer rows.Close()

	columns := make(map[string]string)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("读取列信息失败: %v", err)
		}
		columns[name] = colType
	}

	expectedCols := map[string]string{
		"key":          "TEXT",
		"token":        "TEXT",
		"locked_until": "INTEGER",
	}
	for col, typ := range expectedCols {
		if got, ok := columns[col]; !ok {
			t.Fatalf("缺少列: %s", col)
		} else if got != typ {
			t.Fatalf("列 %s 类型=%s, 期望 %s", col, got, typ)
		}
	}
}
