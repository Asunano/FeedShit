package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

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
		if username, role, ok := sm.Validate(token); ok {
			c.Set("admin_user", username)
			c.Set("admin_role", role)
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

// RequireRole returns a middleware that restricts access to users with the specified role level.
// Role hierarchy: admin > manager > editor > viewer.
// "viewer" can only access routes with no role restriction or viewer-level.
// "editor" can access editor and viewer routes.
// "manager" can access manager, editor, and viewer routes.
// "admin" can access all routes.
func RequireRole(minRole string) gin.HandlerFunc {
	roleLevel := map[string]int{"viewer": 1, "editor": 2, "manager": 3, "admin": 4}
	minLevel := roleLevel[minRole]
	return func(c *gin.Context) {
		role, _ := c.Get("admin_role")
		r, _ := role.(string)
		if roleLevel[r] < minLevel {
			c.JSON(http.StatusForbidden, gin.H{"error": "权限不足"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// ========== PoW Nonce Replay Protection ==========

type NonceCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

func NewNonceCache() *NonceCache {
	nc := &NonceCache{entries: make(map[string]time.Time)}
	go nc.cleanupLoop()
	return nc
}

// CheckAndStore returns true if the nonce is new (not replayed), and stores it.
func (nc *NonceCache) CheckAndStore(key string) bool {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	if _, exists := nc.entries[key]; exists {
		return false // replay detected
	}
	nc.entries[key] = time.Now()
	return true
}

func (nc *NonceCache) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		nc.mu.Lock()
		cutoff := time.Now().Add(-10 * time.Minute)
		for k, t := range nc.entries {
			if t.Before(cutoff) {
				delete(nc.entries, k)
			}
		}
		nc.mu.Unlock()
	}
}

// ========== Login Brute Force Protection ==========

type LoginAttemptTracker struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	max      int
	window   time.Duration
}

func NewLoginAttemptTracker(maxAttempts int) *LoginAttemptTracker {
	t := &LoginAttemptTracker{
		attempts: make(map[string][]time.Time),
		max:      maxAttempts,
		window:   15 * time.Minute,
	}
	go t.cleanupLoop()
	return t
}

// IsLocked returns true if the IP has too many recent failures.
func (t *LoginAttemptTracker) IsLocked(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := time.Now().Add(-t.window)
	attempts := t.attempts[ip]
	var valid []time.Time
	for _, at := range attempts {
		if at.After(cutoff) {
			valid = append(valid, at)
		}
	}
	t.attempts[ip] = valid
	return len(valid) >= t.max
}

// RecordFailure records a failed login attempt.
func (t *LoginAttemptTracker) RecordFailure(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attempts[ip] = append(t.attempts[ip], time.Now())
}

// ClearFailures clears failed attempts on successful login.
func (t *LoginAttemptTracker) ClearFailures(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.attempts, ip)
}

// FailureCount returns the number of recent failures for an IP.
func (t *LoginAttemptTracker) FailureCount(ip string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := time.Now().Add(-t.window)
	count := 0
	for _, at := range t.attempts[ip] {
		if at.After(cutoff) {
			count++
		}
	}
	return count
}

func (t *LoginAttemptTracker) cleanupLoop() {
	ticker := time.NewTicker(15 * time.Minute)
	for range ticker.C {
		t.mu.Lock()
		cutoff := time.Now().Add(-t.window)
		for ip, attempts := range t.attempts {
			var valid []time.Time
			for _, at := range attempts {
				if at.After(cutoff) {
					valid = append(valid, at)
				}
			}
			if len(valid) == 0 {
				delete(t.attempts, ip)
			} else {
				t.attempts[ip] = valid
			}
		}
		t.mu.Unlock()
	}
}
