package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ========== CSRF Protection ==========

// CSRFMiddleware validates CSRF token on state-changing requests.
// Uses double-submit cookie pattern: csrf_token cookie must match X-CSRF-Token header.
func CSRFMiddleware(sm *SessionManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		// The CSRF-token bootstrap endpoint must be reachable without a token
		// (the client fetches its first token there). It still requires an
		// authenticated session via AuthMiddleware, so this is safe.
		if c.Request.URL.Path == "/api/v1/admin/csrf-token" {
			c.Next()
			return
		}

		// Only check state-changing methods
		if c.Request.Method == "GET" || c.Request.Method == "HEAD" || c.Request.Method == "OPTIONS" {
			c.Next()
			return
		}

		sessionToken, err := c.Cookie("admin_session")
		if err != nil || sessionToken == "" {
			// No session — fail closed (defense-in-depth).
			c.JSON(http.StatusForbidden, gin.H{"error": "CSRF 验证失败，请刷新页面后重试"})
			c.Abort()
			return
		}

		// Verify session is valid
		if _, _, ok := sm.Validate(sessionToken); !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "CSRF 验证失败，请刷新页面后重试"})
			c.Abort()
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

		// F17: Rotate CSRF token after successful validation
		c.Next()

		// Only rotate on successful responses (2xx)
		if c.Writer.Status() >= 200 && c.Writer.Status() < 300 {
			newToken := sm.RotateCSRFToken(sessionToken)
			if newToken != "" {
				secure := false
				if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
					secure = true
				}
				SetCSRFCookie(c, newToken, secure)
			}
		}
	}
}

// SetCSRFCookie sets a non-HttpOnly CSRF cookie after successful login.
func SetCSRFCookie(c *gin.Context, csrfToken string, secure bool) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("csrf_token", csrfToken, 86400, "/", "", secure, false)
}
