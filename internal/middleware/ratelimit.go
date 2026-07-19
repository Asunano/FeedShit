package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ========== IP Rate Limiter ==========

type RateLimiter struct {
	mu       sync.Mutex
	records  map[string][]time.Time
	maxPerHR int
}

func NewRateLimiter(maxPerHour int) *RateLimiter {
	rl := &RateLimiter{
		records:  make(map[string][]time.Time),
		maxPerHR: maxPerHour,
	}
	go rl.cleanupLoop()
	return rl
}

func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-1 * time.Hour)

	times := rl.records[ip]
	var valid []time.Time
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.maxPerHR {
		rl.records[ip] = valid
		return false
	}

	rl.records[ip] = append(valid, now)
	return true
}

func (rl *RateLimiter) Count(ip string) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := time.Now().Add(-1 * time.Hour)
	count := 0
	for _, t := range rl.records[ip] {
		if t.After(cutoff) {
			count++
		}
	}
	return count
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(15 * time.Minute)
	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-1 * time.Hour)
		for ip, times := range rl.records {
			var valid []time.Time
			for _, t := range times {
				if t.After(cutoff) {
					valid = append(valid, t)
				}
			}
			if len(valid) == 0 {
				delete(rl.records, ip)
			} else {
				rl.records[ip] = valid
			}
		}
		rl.mu.Unlock()
	}
}

func RateLimitMiddleware(rl *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := GetClientIP(c)
		if !rl.Allow(ip) {
			remaining := rl.Count(ip)
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":     "提交频率超限，每小时最多允许 " + strconv.Itoa(rl.maxPerHR) + " 次",
				"submitted": remaining,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}
