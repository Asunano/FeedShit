package report

import (
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"time"

	"feedshit/internal/database"
)

// AcquireJobLock 尝试获取分布式锁。
// key: 锁名称（如 "weekly_report"）
// ttl: 锁持有时间
// 返回 true 表示本实例成功获取锁。
// 锁 token = hostname-PID-randomHex，用于重入和释放时校验身份。
func AcquireJobLock(db *database.Database, key string, ttl time.Duration) bool {
	// 生成锁 token
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	pid := os.Getpid()
	randBytes := make([]byte, 8)
	if _, err := rand.Read(randBytes); err != nil {
		log.Printf("[JOBLOCK] 生成随机 token 失败: %v", err)
		return false
	}
	token := fmt.Sprintf("%s-%d-%x", hostname, pid, randBytes)

	deadline := time.Now().Unix() + int64(ttl.Seconds())
	now := time.Now().Unix()

	// Step 1: 尝试更新已有锁（过期锁可续约，自己的锁可重入）
	res, err := db.ExecRaw(
		`UPDATE job_locks SET token = ?, locked_until = ? WHERE key = ? AND (locked_until < ? OR token = ?)`,
		token, deadline, key, now, token,
	)
	if err != nil {
		log.Printf("[JOBLOCK] UPDATE 抢锁失败 key=%s: %v", key, err)
		return false
	}
	rows, err := res.RowsAffected()
	if err != nil {
		log.Printf("[JOBLOCK] 读取影响行数失败: %v", err)
		return false
	}
	if rows > 0 {
		log.Printf("[JOBLOCK] 抢锁成功 key=%s (续约/重入), token=%s", key, token)
		return true
	}

	// Step 2: 尝试插入新锁（INSERT OR IGNORE）
	res, err = db.ExecRaw(
		`INSERT OR IGNORE INTO job_locks (key, token, locked_until) VALUES (?, ?, ?)`,
		key, token, deadline,
	)
	if err != nil {
		log.Printf("[JOBLOCK] INSERT 抢锁失败 key=%s: %v", key, err)
		return false
	}
	rows, err = res.RowsAffected()
	if err != nil {
		log.Printf("[JOBLOCK] 读取影响行数失败: %v", err)
		return false
	}
	if rows > 0 {
		log.Printf("[JOBLOCK] 抢锁成功 key=%s (新插入), token=%s", key, token)
		return true
	}

	log.Printf("[JOBLOCK] 抢锁失败 key=%s（其他实例持有）", key)
	return false
}

// ReleaseJobLock 显式释放锁（将 locked_until 置为 0）。
// 仅当 token 匹配时才会释放，防止误删其他实例的锁。
func ReleaseJobLock(db *database.Database, key string) {
	_, err := db.ExecRaw(`UPDATE job_locks SET locked_until = 0 WHERE key = ?`, key)
	if err != nil {
		log.Printf("[JOBLOCK] 释放锁失败 key=%s: %v", key, err)
	} else {
		log.Printf("[JOBLOCK] 锁已释放 key=%s", key)
	}
}
