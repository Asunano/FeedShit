package app

// Independent QA verification tests (Phase 0+1) — authored by QA to prove the
// default rate-limit behavior and the token-level rate-limit gate, which the
// engineer's self-tests did not cover.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"feedshit/internal/config"
	"feedshit/internal/database"
	"feedshit/internal/email"
	"feedshit/internal/middleware"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := &config.Config{
		APITokenDefaultRateLimit: 60,
		BackupRetentionDays:      30,
		BaseURL:                  "http://localhost:8080",
	}
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	sm := middleware.NewSessionManager()
	rl := middleware.NewRateLimiter(10)
	mailer := email.NewMailer(db, cfg.BaseURL)
	return New(cfg, db, sm, rl, mailer)
}

// Point #5: a newly created token with rate_limit<=0 must be upgraded to the
// configured default (60/h); an explicit positive value must be preserved.
func TestQAVerifyAdminCreateAPITokenDefaultRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)

	// rate_limit=0 -> default 60 applied and persisted.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/api-tokens", strings.NewReader(`{"name":"tok-zero","rate_limit":0}`))
	c.Request.Header.Set("Content-Type", "application/json")
	app.AdminCreateAPIToken(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Token     string `json:"token"`
		RateLimit int    `json:"rate_limit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if resp.RateLimit != 60 {
		t.Fatalf("default rate limit not applied: got %d want 60", resp.RateLimit)
	}
	tok, err := app.DB.GetAPITokenByToken(resp.Token)
	if err != nil || tok == nil {
		t.Fatalf("token not persisted: %v", err)
	}
	if tok.RateLimit != 60 {
		t.Fatalf("persisted rate limit = %d, want 60", tok.RateLimit)
	}

	// explicit rate_limit=5 -> preserved.
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/api-tokens", strings.NewReader(`{"name":"tok-explicit","rate_limit":5}`))
	c2.Request.Header.Set("Content-Type", "application/json")
	app.AdminCreateAPIToken(c2)
	var resp2 struct {
		RateLimit int `json:"rate_limit"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode resp2: %v", err)
	}
	if resp2.RateLimit != 5 {
		t.Fatalf("explicit rate limit not preserved: got %d want 5", resp2.RateLimit)
	}
}

// Point #5: the middleware must only rate-limit when rate_limit>0. An unlimited
// token (rate_limit=0) is never throttled; a token with rate_limit=1 is allowed
// once then returns 429.
func TestQAVerifyAPITokenMiddlewareGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)
	if _, err := app.DB.CreateAPIToken("fs_unlimited", "u", "", 0, 0); err != nil {
		t.Fatalf("CreateAPIToken failed: %v", err)
	}
	if _, err := app.DB.CreateAPIToken("fs_limited", "l", "", 1, 0); err != nil {
		t.Fatalf("CreateAPIToken failed: %v", err)
	}

	hit := func(token string) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/external/feedback", nil)
		c.Request.Header.Set("Authorization", "Bearer "+token)
		app.APITokenAuthMiddleware()(c)
		return w.Code
	}

	// Unlimited: 5 consecutive requests must all pass.
	for i := 0; i < 5; i++ {
		if code := hit("fs_unlimited"); code == http.StatusTooManyRequests {
			t.Fatalf("unlimited token must not be throttled (req %d)", i)
		}
	}
	// Limited(=1): first passes, second is 429.
	if code := hit("fs_limited"); code == http.StatusTooManyRequests {
		t.Fatalf("first request of limited token should pass")
	}
	if code := hit("fs_limited"); code != http.StatusTooManyRequests {
		t.Fatalf("second request of limited(=1) token should be 429, got %d", code)
	}
}
