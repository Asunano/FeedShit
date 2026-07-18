package middleware

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ========== Session-based Auth ==========

type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]sessionEntry
}

type sessionEntry struct {
	username string
	expiry   time.Time
}

func NewSessionManager() *SessionManager {
	sm := &SessionManager{sessions: make(map[string]sessionEntry)}
	go sm.cleanupLoop()
	return sm
}

func (sm *SessionManager) Create(username string) string {
	token := generateToken(32)
	sm.mu.Lock()
	sm.sessions[token] = sessionEntry{
		username: username,
		expiry:   time.Now().Add(24 * time.Hour),
	}
	sm.mu.Unlock()
	return token
}

func (sm *SessionManager) Validate(token string) (string, bool) {
	sm.mu.RLock()
	entry, ok := sm.sessions[token]
	sm.mu.RUnlock()
	if !ok || time.Now().After(entry.expiry) {
		if ok {
			sm.mu.Lock()
			delete(sm.sessions, token)
			sm.mu.Unlock()
		}
		return "", false
	}
	return entry.username, true
}

func (sm *SessionManager) Revoke(token string) {
	sm.mu.Lock()
	delete(sm.sessions, token)
	sm.mu.Unlock()
}

func (sm *SessionManager) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	for range ticker.C {
		sm.mu.Lock()
		now := time.Now()
		for token, entry := range sm.sessions {
			if now.After(entry.expiry) {
				delete(sm.sessions, token)
			}
		}
		sm.mu.Unlock()
	}
}

func generateToken(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// AuthMiddleware checks for a valid session cookie on admin routes.
func AuthMiddleware(sm *SessionManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie("admin_session")
		if err != nil || token == "" {
			if isAPIRoute(c.Request.URL.Path) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
				c.Abort()
			} else {
				c.Redirect(http.StatusFound, "/admin/login")
			}
			return
		}
		if username, ok := sm.Validate(token); ok {
			c.Set("admin_user", username)
			c.Set("session_token", token)
			c.Next()
		} else {
			if isAPIRoute(c.Request.URL.Path) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "会话已过期"})
				c.Abort()
			} else {
				c.Redirect(http.StatusFound, "/admin/login")
			}
		}
	}
}

func isAPIRoute(path string) bool {
	return strings.HasPrefix(path, "/api/")
}

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

// ========== Proof of Work Verification ==========

func VerifyPoW(projectID, timestamp, nonce string, difficulty int) bool {
	// Validate timestamp first (cheap check before expensive hash)
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	serverTime := time.Now().Unix()
	diff := serverTime - ts
	if diff < 0 {
		diff = -diff
	}
	if diff > 300 {
		return false
	}

	// Now compute hash
	payload := projectID + timestamp + nonce
	hash := sha256.Sum256([]byte(payload))
	hashHex := hex.EncodeToString(hash[:])

	prefix := strings.Repeat("0", difficulty)
	if !strings.HasPrefix(hashHex, prefix) {
		return false
	}

	return true
}

// ========== Helpers ==========

func SecureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func GetClientIP(c *gin.Context) string {
	// CF-Connecting-IP: Cloudflare always sets this, most trustworthy
	if cf := c.GetHeader("CF-Connecting-IP"); cf != "" {
		return cf
	}
	// X-Forwarded-For: take the first (client) IP from the chain
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	// X-Real-Ip: commonly set by nginx
	if xri := c.GetHeader("X-Real-Ip"); xri != "" {
		return xri
	}
	// Forwarded: RFC 7239 standard header — parse "for=..." directive
	if fwd := c.GetHeader("Forwarded"); fwd != "" {
		for _, param := range strings.Split(fwd, ";") {
			param = strings.TrimSpace(param)
			if strings.HasPrefix(strings.ToLower(param), "for=") {
				forVal := param[4:]
				// Strip quotes and brackets (IPv6 notation)
				forVal = strings.Trim(forVal, `"[]`)
				// Handle [IPv6]:port format
				if idx := strings.LastIndex(forVal, "]:"); idx > 0 {
					forVal = forVal[:idx+1]
				}
				forVal = strings.Trim(forVal, `"[]`)
				if forVal != "" {
					return forVal
				}
			}
		}
	}
	return c.ClientIP()
}

func FormatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div := int64(unit)
	val := float64(bytes) / float64(div)
	for _, u := range []string{"KB", "MB", "GB"} {
		div *= unit
		if bytes < div || u == "GB" {
			return fmt.Sprintf("%.1f %s", val, u)
		}
		val = float64(bytes) / float64(div)
	}
	return ""
}
