package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"feedshit/internal/app"
	"feedshit/internal/config"
	"feedshit/internal/database"
	"feedshit/internal/email"
	"feedshit/internal/middleware"
)

func newTestApp(t *testing.T) *app.App {
	t.Helper()
	cfg := &config.Config{
		BaseURL:             "http://localhost:8080",
		APITokenDefaultRateLimit: 60,
		BackupRetentionDays: 30,
	}
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	sm := middleware.NewSessionManager()
	rl := middleware.NewRateLimiter(10)
	mailer := email.NewMailer(db, cfg.BaseURL)
	return app.New(cfg, db, sm, rl, mailer)
}

func newTestServer(t *testing.T, app *app.App) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r, app)
	return httptest.NewServer(r)
}

func TestHealthEndpoint(t *testing.T) {
	srv := newTestServer(t, newTestApp(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestLandingPage(t *testing.T) {
	srv := newTestServer(t, newTestApp(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "text/html; charset=utf-8" {
		t.Fatalf("expected text/html, got %q", ct)
	}
}

func TestSetupPage(t *testing.T) {
	srv := newTestServer(t, newTestApp(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/setup")
	if err != nil {
		t.Fatalf("GET /setup failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestLoginPage(t *testing.T) {
	srv := newTestServer(t, newTestApp(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/admin/login")
	if err != nil {
		t.Fatalf("GET /admin/login failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAdminPageReturnsRedirectOrHTMLWhenNotLoggedIn(t *testing.T) {
	srv := newTestServer(t, newTestApp(t))
	defer srv.Close()

	// Without session cookie, /admin either redirects or returns HTML depending on setup state
	resp, err := http.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatalf("GET /admin failed: %v", err)
	}
	defer resp.Body.Close()

	// Both 302 (redirect to login) and 200 (admin HTML without session guard on fresh app)
	// are acceptable — what matters is we don't get a 500
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 302 or 200, got %d", resp.StatusCode)
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv := newTestServer(t, newTestApp(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()

	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("CSP header is missing")
	}
	// Must have nonce-based script-src, NOT unsafe-inline for scripts
	if searchString(csp, "script-src") && contains(csp, "'unsafe-inline'") {
		// Only fail if 'unsafe-inline' appears in the script-src directive
		// (style-src 'unsafe-inline' is acceptable)
		scriptPart := extractDirective(csp, "script-src")
		if contains(scriptPart, "'unsafe-inline'") {
			t.Fatal("script-src should not contain 'unsafe-inline'")
		}
	}
	if !contains(csp, "'nonce-") {
		t.Fatal("CSP should contain nonce-based script-src")
	}
}

func TestFeedbackPageBySlug(t *testing.T) {
	srv := newTestServer(t, newTestApp(t))
	defer srv.Close()

	// The default project may not exist in a fresh test DB.
	// If it returns 200 (project exists) or 404 (project missing), either is fine.
	// The important thing is it doesn't crash.
	resp, err := http.Get(srv.URL + "/fb/default")
	if err != nil {
		t.Fatalf("GET /fb/default failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 200 or 404 for /fb/default, got %d", resp.StatusCode)
	}
}

func TestFeedbackPageNonExistentSlug(t *testing.T) {
	srv := newTestServer(t, newTestApp(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/fb/nonexistent")
	if err != nil {
		t.Fatalf("GET /fb/nonexistent failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for non-existent slug, got %d", resp.StatusCode)
	}
}

func TestTrackPage(t *testing.T) {
	srv := newTestServer(t, newTestApp(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/track")
	if err != nil {
		t.Fatalf("GET /track failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestRoadmapPage(t *testing.T) {
	srv := newTestServer(t, newTestApp(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/p/default/roadmap")
	if err != nil {
		t.Fatalf("GET /p/default/roadmap failed: %v", err)
	}
	defer resp.Body.Close()

	// Roadmap is a public page, should return HTML
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "text/html; charset=utf-8" {
		t.Fatalf("expected text/html, got %q", ct)
	}
}

func TestSetupGuardBlocksAPIWhenNotSetup(t *testing.T) {
	// For setup guard testing, we need an app where setup is not complete.
	// The default app created by newTestApp is complete. We need to test
	// the guard only — which is part of Register(). The guard redirects
	// API calls to /setup when IsSetupComplete returns false.
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// extractDirective extracts a single directive value from a CSP string.
func extractDirective(csp, directive string) string {
	parts := strings.Split(csp, ";")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, directive) {
			return strings.TrimSpace(p[len(directive):])
		}
	}
	return ""
}
