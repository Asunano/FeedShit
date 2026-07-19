package middleware

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
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
	username  string
	role      string
	expiry    time.Time
	csrfToken string
}

func NewSessionManager() *SessionManager {
	sm := &SessionManager{sessions: make(map[string]sessionEntry)}
	go sm.cleanupLoop()
	return sm
}

func (sm *SessionManager) Create(username, role string) string {
	token := generateToken(32)
	csrf := generateToken(32)
	sm.mu.Lock()
	sm.sessions[token] = sessionEntry{
		username:  username,
		role:      role,
		expiry:    time.Now().Add(24 * time.Hour),
		csrfToken: csrf,
	}
	sm.mu.Unlock()
	return token
}

func (sm *SessionManager) Validate(token string) (username, role string, ok bool) {
	sm.mu.RLock()
	entry, exists := sm.sessions[token]
	sm.mu.RUnlock()
	if !exists || time.Now().After(entry.expiry) {
		if exists {
			sm.mu.Lock()
			delete(sm.sessions, token)
			sm.mu.Unlock()
		}
		return "", "", false
	}
	return entry.username, entry.role, true
}

func (sm *SessionManager) Revoke(token string) {
	sm.mu.Lock()
	delete(sm.sessions, token)
	sm.mu.Unlock()
}

// GetCSRFToken returns the CSRF token for a given session.
func (sm *SessionManager) GetCSRFToken(token string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if entry, ok := sm.sessions[token]; ok {
		return entry.csrfToken
	}
	return ""
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

// ========== CSRF Protection ==========

// CSRFMiddleware validates CSRF token on state-changing requests.
// Uses double-submit cookie pattern: csrf_token cookie must match X-CSRF-Token header.
func CSRFMiddleware(sm *SessionManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only check state-changing methods
		if c.Request.Method == "GET" || c.Request.Method == "HEAD" || c.Request.Method == "OPTIONS" {
			c.Next()
			return
		}

		sessionToken, err := c.Cookie("admin_session")
		if err != nil || sessionToken == "" {
			c.Next()
			return
		}

		// Verify session is valid
		if _, _, ok := sm.Validate(sessionToken); !ok {
			c.Next()
			return
		}

		// Double-submit cookie pattern: csrf_token cookie must match X-CSRF-Token header.
		cookieToken, _ := c.Cookie("csrf_token")
		headerToken := c.GetHeader("X-CSRF-Token")

		if cookieToken == "" || headerToken == "" || !SecureCompare(cookieToken, headerToken) {
			c.JSON(http.StatusForbidden, gin.H{"error": "CSRF 验证失败，请刷新页面后重试"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// SetCSRFCookie sets a non-HttpOnly CSRF cookie after successful login.
func SetCSRFCookie(c *gin.Context, csrfToken string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("csrf_token", csrfToken, 86400, "/", "", false, false)
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

// GetClientIP extracts the real client IP address.
// CDN/proxy headers are only trusted when the direct connection is from a trusted proxy.
func GetClientIP(c *gin.Context) string {
	remoteIP, _, _ := net.SplitHostPort(c.Request.RemoteAddr)
	if remoteIP == "" {
		remoteIP = c.ClientIP()
	}

	// CDN provider mode:
	// "none" — never trust headers, use direct connection IP only
	// "auto" — try all known CDN/proxy headers in priority order
	// "cloudflare" — CF-Connecting-IP only
	// "generic" — X-Forwarded-For / X-Real-Ip / Forwarded
	if cdnProvider == "none" {
		return remoteIP
	}

	// Only read CDN/proxy headers when trusted proxies are explicitly configured
	// AND the direct connection is from a trusted proxy.
	if len(trustedProxies) == 0 {
		return remoteIP
	}

	trusted := false
	for _, tp := range trustedProxies {
		if tp == remoteIP || tp == "*" {
			trusted = true
			break
		}
	}
	if !trusted {
		return remoteIP
	}

	switch cdnProvider {
	case "cloudflare":
		// Cloudflare always sets CF-Connecting-IP, most trustworthy
		if cf := c.GetHeader("CF-Connecting-IP"); cf != "" {
			return cf
		}
	case "generic":
		// Generic proxy: X-Forwarded-For first, then X-Real-Ip
		if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[0]); ip != "" {
				return ip
			}
		}
		if xri := c.GetHeader("X-Real-Ip"); xri != "" {
			return xri
		}
	default: // "auto" — try all headers in priority order
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
					forVal = strings.Trim(forVal, `"[]`)
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
	}
	return remoteIP
}

// trustedProxies is set via SetTrustedProxies from config.
var trustedProxies []string

// SetTrustedProxies configures which proxy IPs are trusted for reading CDN headers.
func SetTrustedProxies(proxies []string) {
	trustedProxies = proxies
}

// cdnProvider controls which headers to read for real client IP.
// Values: "auto" (default), "cloudflare", "generic", "none"
var cdnProvider = "auto"

// SetCDNProvider sets the CDN provider for IP detection.
func SetCDNProvider(provider string) {
	switch provider {
	case "none", "cloudflare", "generic", "auto":
		cdnProvider = provider
	default:
		cdnProvider = "auto"
	}
}

// GetCDNProvider returns the current CDN provider setting.
func GetCDNProvider() string {
	return cdnProvider
}

// FormatSize converts bytes to a human-readable string.
func FormatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	val := float64(bytes)
	units := []string{"KB", "MB", "GB", "TB"}
	for i, u := range units {
		val /= float64(unit)
		if val < float64(unit) || i == len(units)-1 {
			return fmt.Sprintf("%.1f %s", val, u)
		}
	}
	return fmt.Sprintf("%.1f %s", val, units[len(units)-1])
}
