package app

// M9 FAQ self-service handler / endpoint regression tests.
// Covers acceptance items: public endpoint security & shape (2), admin
// boundary codes (3), admin list inactive flag (4) and audit logging (5).
// DB-layer behaviour (isolation, inactive filtering, hard delete, UNIQUE, LIKE,
// migration) lives in internal/database/faq_test.go.
//
// Note on 403 (unauthorized / non-editor): this is enforced by the route-level
// RequireRole middleware, which is mounted in internal/routes/routes.go and is
// covered by the existing middleware test suite. It is therefore validated via
// `go build ./...` (route wiring) + the middleware tests, not a session-injected
// unit test here.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"feedshit/internal/config"
	"feedshit/internal/database"
	"feedshit/internal/email"
	"feedshit/internal/middleware"
)

func mustCreateProject(t *testing.T, db *database.Database, slug string) {
	t.Helper()
	if _, err := db.CreateProject(&database.Project{Name: slug, Slug: slug, IsActive: true}); err != nil {
		t.Fatalf("CreateProject(%s): %v", slug, err)
	}
}

// Item 2: empty q or project returns {faqs:[]} with no error.
func TestPublicSearchFAQEmptyParams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/faq?project=proj-a", nil)
	app.PublicSearchFAQ(c)
	if w.Code != http.StatusOK {
		t.Fatalf("empty q: expected 200, got %d", w.Code)
	}
	var empty struct {
		Faqs []database.PublicFAQ `json:"faqs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &empty); err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	if empty.Faqs == nil || len(empty.Faqs) != 0 {
		t.Fatalf("empty q should yield {faqs:[]}, got %s", w.Body.String())
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/v1/faq?q=hello", nil)
	app.PublicSearchFAQ(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("empty project: expected 200, got %d", w2.Code)
	}
}

// Item 2: response shape is exactly {id,question,answer}; project_slug and
// embedding must never leak.
func TestPublicSearchFAQStructureNoLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)
	if _, err := app.DB.CreateFAQ("proj-a", "How to export?", "Go to settings.", 0, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/faq?q=export&project=proj-a", nil)
	app.PublicSearchFAQ(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	raw := w.Body.String()
	if strings.Contains(raw, "project_slug") {
		t.Fatalf("response leaks project_slug: %s", raw)
	}
	if strings.Contains(raw, "embedding") {
		t.Fatalf("response leaks embedding: %s", raw)
	}
	var body struct {
		Faqs []database.PublicFAQ `json:"faqs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Faqs) != 1 {
		t.Fatalf("expected 1 result, got %d", len(body.Faqs))
	}
	if body.Faqs[0].ID == 0 || body.Faqs[0].Question == "" || body.Faqs[0].Answer == "" {
		t.Fatalf("incomplete PublicFAQ: %+v", body.Faqs[0])
	}
}

// Item 2: hostile / wildcard q values must not error and must not leak rows
// from other projects (parameterized query, no injection).
func TestPublicSearchFAQSpecialChars(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)
	if _, err := app.DB.CreateFAQ("proj-a", "Reset password", "ans", 0, true); err != nil {
		t.Fatalf("seed proj-a: %v", err)
	}
	if _, err := app.DB.CreateFAQ("proj-b", "Other", "ans", 0, true); err != nil {
		t.Fatalf("seed proj-b: %v", err)
	}
	for _, q := range []string{`' OR 1=1 --`, `%`, `_`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/faq?q="+url.QueryEscape(q)+"&project=proj-a", nil)
		app.PublicSearchFAQ(c)
		if w.Code != http.StatusOK {
			t.Fatalf("q=%q: expected 200, got %d body=%s", q, w.Code, w.Body.String())
		}
		var body struct {
			Faqs []database.PublicFAQ `json:"faqs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("q=%q decode: %v", q, err)
		}
		for _, f := range body.Faqs {
			if f.Question != "Reset password" {
				t.Fatalf("q=%q: cross-project leak / unexpected row: %+v", q, f)
			}
		}
	}
}

// Item 2: high-frequency requests trigger 429 on the same rate-limiter instance
// and the server keeps responding (no crash).
func TestPublicSearchFAQRateLimit429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{APITokenDefaultRateLimit: 60, BackupRetentionDays: 30, BaseURL: "http://localhost:8080"}
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase: %v", err)
	}
	sm := middleware.NewSessionManager()
	rl := middleware.NewRateLimiter(5) // low threshold so the test stays fast
	mailer := email.NewMailer(db, cfg.BaseURL)
	app := New(cfg, db, sm, rl, mailer)

	r := gin.New()
	r.Use(middleware.RateLimitMiddleware(app.RL))
	r.GET("/api/v1/faq", app.PublicSearchFAQ)

	var lastCode int
	for i := 0; i < 6; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/faq?q=x&project=proj-a", nil)
		req.RemoteAddr = "203.0.113.9:5555"
		r.ServeHTTP(w, req)
		lastCode = w.Code
		if i < 5 && w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d should be allowed, got 429", i)
		}
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on the 6th request, got %d", lastCode)
	}
}

// Item 3: empty question -> 400.
func TestAdminCreateFAQEmptyQuestion400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)
	mustCreateProject(t, app.DB, "proj-x")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// Path params are only bound by the router; when calling the handler
	// directly we must set them explicitly.
	c.Params = gin.Params{{Key: "slug", Value: "proj-x"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/projects/proj-x/faqs", strings.NewReader(`{"question":"   ","answer":"a"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	app.AdminCreateFAQ(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty question: expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

// Item 3: duplicate question in same project -> 409.
func TestAdminCreateFAQDuplicate409(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)
	mustCreateProject(t, app.DB, "proj-x")
	if _, err := app.DB.CreateFAQ("proj-x", "Same Q?", "a", 0, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "slug", Value: "proj-x"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/projects/proj-x/faqs", strings.NewReader(`{"question":"Same Q?","answer":"b"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	app.AdminCreateFAQ(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate question: expected 409, got %d body=%s", w.Code, w.Body.String())
	}
}

// Item 3: update of non-existent id -> 404.
func TestAdminUpdateFAQNotFound404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)
	mustCreateProject(t, app.DB, "proj-x")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "slug", Value: "proj-x"}, {Key: "id", Value: "99999999"}}
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/projects/proj-x/faqs/99999999", strings.NewReader(`{"question":"Q","answer":"a"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	app.AdminUpdateFAQ(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing id update: expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// Item 3: delete of non-existent id -> 404.
func TestAdminDeleteFAQNotFound404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)
	mustCreateProject(t, app.DB, "proj-x")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "slug", Value: "proj-x"}, {Key: "id", Value: "99999999"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/projects/proj-x/faqs/99999999", nil)
	app.AdminDeleteFAQ(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing id delete: expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// Item 4: admin list returns inactive FAQs flagged with is_active=false.
func TestAdminListFAQsInactiveFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)
	mustCreateProject(t, app.DB, "proj-x")
	id, err := app.DB.CreateFAQ("proj-x", "Inactive listed?", "a", 0, false)
	if err != nil {
		t.Fatalf("seed inactive: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "slug", Value: "proj-x"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/projects/proj-x/faqs", nil)
	app.AdminListFAQs(c)
	if w.Code != http.StatusOK {
		t.Fatalf("admin list: expected 200, got %d", w.Code)
	}
	var body struct {
		Faqs []database.FAQ `json:"faqs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, f := range body.Faqs {
		if f.ID == id {
			found = true
			if f.IsActive {
				t.Fatalf("inactive FAQ must report is_active=false")
			}
		}
	}
	if !found {
		t.Fatalf("inactive FAQ not present in admin list")
	}
}

// Item 5: create / update / delete each write the matching audit_logs entry.
func TestAdminCRUDAuditLogs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)
	mustCreateProject(t, app.DB, "proj-x")

	// create
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "slug", Value: "proj-x"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/projects/proj-x/faqs", strings.NewReader(`{"question":"Audit Q?","answer":"a","is_active":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	app.AdminCreateFAQ(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	var cresp struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cresp); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	id := cresp.ID
	if id <= 0 {
		t.Fatalf("create did not return id")
	}

	// update
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Params = gin.Params{{Key: "slug", Value: "proj-x"}, {Key: "id", Value: strconv.FormatInt(id, 10)}}
	c2.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/projects/proj-x/faqs/"+strconv.FormatInt(id, 10), strings.NewReader(`{"question":"Audit Q?","answer":"updated"}`))
	c2.Request.Header.Set("Content-Type", "application/json")
	app.AdminUpdateFAQ(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d body=%s", w2.Code, w2.Body.String())
	}

	// delete
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Params = gin.Params{{Key: "slug", Value: "proj-x"}, {Key: "id", Value: strconv.FormatInt(id, 10)}}
	c3.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/projects/proj-x/faqs/"+strconv.FormatInt(id, 10), nil)
	app.AdminDeleteFAQ(c3)
	if w3.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d body=%s", w3.Code, w3.Body.String())
	}

	logs, _, err := app.DB.ListAuditLogs(100, 0)
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	seen := map[string]bool{}
	for _, l := range logs {
		seen[l.Action] = true
	}
	for _, act := range []string{"create_faq", "update_faq", "delete_faq"} {
		if !seen[act] {
			t.Fatalf("audit log missing action %q (got %v)", act, seen)
		}
	}
}
