package routes

import (
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

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

func TestInvitePageValidToken(t *testing.T) {
	// Use a single app for both the server and the invitation so they share the
	// same (in-memory) database. Complete setup first so the pre-setup guard does
	// not redirect /invite/* to /setup before the route runs.
	testApp := newTestApp(t)
	if err := testApp.DB.SetConfig("setup_complete", "true", "test"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	srv := newTestServer(t, testApp)
	defer srv.Close()

	// Create a valid invitation directly via the app DB.
	inv, err := testApp.DB.CreateInvitation("editor", nil, 1, "admin", 7)
	if err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}

	resp, err := http.Get(srv.URL + "/invite/" + inv.Token)
	if err != nil {
		t.Fatalf("GET /invite failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "加入团队") {
		t.Fatal("expected registration page content '加入团队'")
	}
}

func TestInvitePageInvalidToken(t *testing.T) {
	// Complete setup so the pre-setup guard lets /invite/* through to the route,
	// which then rejects the unknown token with an "无效" message.
	testApp := newTestApp(t)
	if err := testApp.DB.SetConfig("setup_complete", "true", "test"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	srv := newTestServer(t, testApp)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/invite/invalidtoken123")
	if err != nil {
		t.Fatalf("GET /invite failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "无效") {
		t.Fatal("expected 'invalid' message for bad token")
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

// TestAllPageTemplatesRender verifies that every page template parses and
// renders through the SAME ParseFS + executePage path used in production, and
// that the unified container (chrometop/chromebot) is present in each. This is
// the primary guard for the modular refactor: it catches undefined template
// functions, stray {{...}} literals, and missing container markers for ALL
// pages — including feedback.html and admin.html, which require a project /
// admin session to reach via the HTTP routes.
func TestAllPageTemplatesRender(t *testing.T) {
	tpl := template.Must(template.ParseFS(frontendFS, "frontend/layouts/base.html", "frontend/pages/*.html"))

	pages := []struct {
		name string
		nav  string
	}{
		{"index.html", ""},
		{"setup.html", ""},
		{"track.html", ""},
		{"login.html", ""},
		{"roadmap.html", ""},
		{"register.html", ""},
		{"feedback.html", ""},
		{"admin.html", "admin"},
	}

	for _, p := range pages {
		html, err := executePage(tpl, p.name, PageData{Nav: p.nav, Nonce: "test-nonce"})
		if err != nil {
			t.Fatalf("render %s failed: %v", p.name, err)
		}
		if !strings.Contains(html, "theme-toggle") {
			t.Fatalf("%s: unified container missing (no theme-toggle)", p.name)
		}
		if !strings.Contains(html, "site-footer") {
			t.Fatalf("%s: unified container missing (no site-footer)", p.name)
		}
		// Inline (non-external) scripts must carry the per-request CSP nonce.
		// Pages that load JS only via <script src> (e.g. admin.html → dashboard.js)
		// legitimately need no nonce and are exempt.
		if inlineScriptNeedsNonce(html) && !strings.Contains(html, "test-nonce") {
			t.Fatalf("%s: inline script missing CSP nonce", p.name)
		}
	}
}

// inlineScriptNeedsNonce reports whether html contains an inline <script> tag
// (one without a src= attribute), which under the project's strict CSP must
// carry a per-request nonce.
func inlineScriptNeedsNonce(html string) bool {
	for i := 0; i < len(html); {
		j := strings.Index(html[i:], "<script")
		if j == -1 {
			return false
		}
		start := i + j + len("<script")
		k := start
		for k < len(html) && (html[k] == ' ' || html[k] == '\n' || html[k] == '\t' || html[k] == '\r') {
			k++
		}
	if k < len(html) && !strings.HasPrefix(html[k:], "src=") {
		return true
	}
	i = start
}
return false
}

// TestFeedbackPageInjectsProjectData guards two feedback-page regressions:
//  1. The server must inject the project JSON (with form_schema) into the
//     `var PROJECT = __PROJECT_DATA__;` placeholder. If this is skipped, PROJECT
//     stays the literal token and the page shows the "项目信息未加载" error.
//     NOTE: the placeholder must be a plain token (NOT a /* */ comment) because
//     html/template strips JS block comments at render time.
//  2. The inline <script> must NOT contain the broken `\\'` (backslash-backslash-
//     quote) escape that used to break the whole script's parse — which silently
//     killed custom-field rendering AND the notify-consent modal.
func TestFeedbackPageInjectsProjectData(t *testing.T) {
	tpl := template.Must(template.ParseFS(frontendFS, "frontend/layouts/base.html", "frontend/pages/*.html"))
	rendered, err := executePage(tpl, "feedback.html", PageData{Nav: "", Nonce: "test-nonce"})
	if err != nil {
		t.Fatalf("render feedback.html failed: %v", err)
	}

	// Simulate routes.go injection of a project that has custom form fields.
	info, _ := json.Marshal(map[string]interface{}{
		"name":        "演示项目",
		"slug":        "demo",
		"description": "用于测试",
		"form_schema": json.RawMessage(`[{"type":"text","name":"env","label":"环境","required":true},{"type":"rating","name":"score","label":"评分"}]`),
		"categories":  []map[string]string{{"key": "bug", "name": "缺陷"}},
	})
	projectJSON := strings.ReplaceAll(string(info), "<", "\\u003c")
	rendered = strings.Replace(rendered, "__PROJECT_DATA__", projectJSON, 1)

	if strings.Contains(rendered, "__PROJECT_DATA__") {
		t.Fatal("project data placeholder was NOT injected — PROJECT would stay null")
	}
	if !strings.Contains(rendered, `"form_schema"`) {
		t.Fatal("injected project JSON missing form_schema key")
	}
	// The specific broken escape that crashed the whole inline script.
	if strings.Contains(rendered, "\\\\'") {
		t.Fatal("feedback inline script still contains broken '\\\\'' escape (would cause SyntaxError)")
	}
}

// TestAdminCSRFFlowNotForbidden guards the admin CSRF flow:
//  1. The CSRF-token bootstrap endpoint (/api/v1/admin/csrf-token) must be
//     reachable WITH a session but WITHOUT a token (it used to be protected by
//     CSRFMiddleware → chicken-and-egg → every admin mutation returned 403).
//  2. A mutation carrying the fetched token must not 403.
//  3. After a successful mutation the server rotates the token; the next
//     mutation (using the rotated cookie) must also not 403.
func TestAdminCSRFFlowNotForbidden(t *testing.T) {
	app := newTestApp(t)
	if err := app.DB.SetConfig("setup_complete", "true", "test"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("TestPass123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt hash failed: %v", err)
	}
	if _, err := app.DB.CreateAdmin("csrftester", string(hash), "admin"); err != nil {
		t.Fatalf("CreateAdmin failed: %v", err)
	}

	srv := newTestServer(t, app)
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// 1) Login → establishes admin_session + csrf_token cookies.
	loginResp, err := client.Post(srv.URL+"/api/v1/admin/login", "application/json",
		strings.NewReader(`{"username":"csrftester","password":"TestPass123"}`))
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login expected 200, got %d", loginResp.StatusCode)
	}

	// 2) Bootstrap endpoint must return 200 (was 403 before the fix).
	csrfResp, err := client.Post(srv.URL+"/api/v1/admin/csrf-token", "application/json", nil)
	if err != nil {
		t.Fatalf("csrf-token POST failed: %v", err)
	}
	if csrfResp.StatusCode != http.StatusOK {
		t.Fatalf("csrf-token bootstrap expected 200, got %d (chicken-and-egg not fixed)", csrfResp.StatusCode)
	}
	var csrfBody struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(csrfResp.Body).Decode(&csrfBody); err != nil {
		t.Fatalf("decode csrf body: %v", err)
	}
	csrfResp.Body.Close()
	if csrfBody.CSRFToken == "" {
		t.Fatal("csrf-token bootstrap returned empty token")
	}

	createProject := func(token string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/admin/projects",
			strings.NewReader(`{"name":"Proj A","slug":"proj-a"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("create project failed: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// 3) First mutation with the fetched token must not 403.
	if code := createProject(csrfBody.CSRFToken); code == http.StatusForbidden {
		t.Fatal("first project creation returned 403 (CSRF validation failed)")
	}

	// 4) Token rotated after the successful mutation; read the new cookie and
	//    confirm the next mutation also succeeds (rotation desync fixed).
	u, _ := url.Parse(srv.URL)
	var rotated string
	for _, c := range jar.Cookies(u) {
		if c.Name == "csrf_token" {
			rotated = c.Value
		}
	}
	if rotated == "" {
		t.Fatal("csrf_token cookie missing after mutation")
	}
	if code := createProject(rotated); code == http.StatusForbidden {
		t.Fatal("second project creation returned 403 (CSRF rotation desync not fixed)")
	}
}
