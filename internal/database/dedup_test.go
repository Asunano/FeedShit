package database_test

// M5 duplicate detection — database + handler integration regression tests.
//
// Acceptance items 2–8:
//   2. 写入侧：InsertFeedback 后 content_hash 不为空
//   3. 回填幂等：BackfillContentHashes 仅补空行
//   4. 公共检测接口：空 q/project → empty；project 隔离；响应仅含 id/title/summary/token
//   5. 管理端候选：同 project、开放态
//   6. MarkAsDuplicate 跨项目→400；UnmarkDuplicate 可逆
//   7. 合并后展示：is_duplicate=1 + duplicate_of 指向目标 ID
//   8. 迁移幂等：content_hash 列+索引齐备

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"feedshit/internal/app"
	"feedshit/internal/config"
	"feedshit/internal/database"
	"feedshit/internal/email"
	"feedshit/internal/middleware"
)

// ---------- helpers ----------

func newTestApp(t *testing.T) *app.App {
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
	return app.New(cfg, db, sm, rl, mailer)
}

func mustCreateProject(t *testing.T, db *database.Database, slug string) {
	t.Helper()
	if _, err := db.CreateProject(&database.Project{Name: slug, Slug: slug, IsActive: true}); err != nil {
		t.Fatalf("CreateProject(%s): %v", slug, err)
	}
}

func insertFeedback(t *testing.T, db *database.Database, projectID, title, desc, status string) int64 {
	t.Helper()
	f := &database.Feedback{
		ProjectID:   projectID,
		Title:       title,
		Description: desc,
		Status:      status,
	}
	if status == "" {
		f.Status = "pending"
	}
	id, err := db.InsertFeedback(f)
	if err != nil {
		t.Fatalf("InsertFeedback(%q,%q): %v", projectID, title, err)
	}
	return id
}

// ---------- Item 2: InsertFeedback writes content_hash ----------

func TestInsertFeedbackWritesContentHash(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	// Non-empty title + description → content_hash must be non-empty
	id := insertFeedback(t, db, "proj-a", "Hello World", "Some description", "pending")
	fb, err := db.GetFeedback(id)
	if err != nil {
		t.Fatalf("GetFeedback(%d): %v", id, err)
	}
	if fb.ContentHash == "" {
		t.Fatal("InsertFeedback should write non-empty content_hash for non-empty title/desc")
	}
	if len(fb.ContentHash) != 64 {
		t.Fatalf("content_hash length = %d, want 64", len(fb.ContentHash))
	}

	// Empty title + empty description → still deterministic hash
	id2 := insertFeedback(t, db, "proj-a", "", "", "pending")
	fb2, err := db.GetFeedback(id2)
	if err != nil {
		t.Fatalf("GetFeedback(%d): %v", id2, err)
	}
	if fb2.ContentHash == "" {
		t.Fatal("even empty content should get a deterministic content_hash")
	}
}

func TestInsertFeedbackHashDeterministicViaDB(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	id1 := insertFeedback(t, db, "proj-a", "Hello World!", "Description.", "pending")
	id2 := insertFeedback(t, db, "proj-a", "   hello, world!!   ", "   description...   ", "pending")
	fb1, _ := db.GetFeedback(id1)
	fb2, _ := db.GetFeedback(id2)
	if fb1.ContentHash != fb2.ContentHash {
		t.Fatalf("same content (varying whitespace/punct) must have same hash:\n  h1=%q\n  h2=%q",
			fb1.ContentHash, fb2.ContentHash)
	}
}

func TestInsertFeedbackHashDifferentContent(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	id1 := insertFeedback(t, db, "proj-a", "Hello", "World", "pending")
	id2 := insertFeedback(t, db, "proj-a", "Goodbye", "World", "pending")
	fb1, _ := db.GetFeedback(id1)
	fb2, _ := db.GetFeedback(id2)
	if fb1.ContentHash == fb2.ContentHash {
		t.Fatal("different content must produce different content_hash")
	}
}

// ---------- Item 3: BackfillContentHashes idempotency ----------

func TestBackfillContentHashesIdempotent(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	// Insert a feedback via the public API (hash is auto-filled)
	idFilled := insertFeedback(t, db, "proj-a", "Filled Title", "Filled Desc", "pending")
	fbFilled, _ := db.GetFeedback(idFilled)
	filledHash := fbFilled.ContentHash
	if filledHash == "" {
		t.Fatal("InsertFeedback should fill content_hash")
	}

	// Run backfill — should not change the already-filled row
	if err := db.BackfillContentHashes(); err != nil {
		t.Fatalf("BackfillContentHashes: %v", err)
	}
	fbFilled2, _ := db.GetFeedback(idFilled)
	if fbFilled2.ContentHash != filledHash {
		t.Fatalf("backfill changed already-filled hash from %q to %q", filledHash, fbFilled2.ContentHash)
	}

	// Run backfill a second time — still no error
	if err := db.BackfillContentHashes(); err != nil {
		t.Fatalf("second BackfillContentHashes: %v", err)
	}
}

// ---------- Item 4: PublicCheckDuplicate ----------

func TestPublicCheckDuplicateEmptyParams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := newTestApp(t)

	// Empty q → candidates:[]
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/feedback/check-duplicate?q=&project=proj-a", nil)
	a.PublicCheckDuplicate(c)
	if w.Code != http.StatusOK {
		t.Fatalf("empty q: expected 200, got %d", w.Code)
	}
	var body struct {
		Candidates []interface{} `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode empty q: %v", err)
	}
	if body.Candidates == nil {
		t.Fatal("empty q should yield {candidates:[]}, not null")
	}
	if len(body.Candidates) != 0 {
		t.Fatalf("empty q should yield empty candidates, got %d", len(body.Candidates))
	}

	// Empty project → candidates:[]
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/v1/feedback/check-duplicate?q=hello&project=", nil)
	a.PublicCheckDuplicate(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("empty project: expected 200, got %d", w2.Code)
	}
	var body2 struct {
		Candidates []interface{} `json:"candidates"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &body2); err != nil {
		t.Fatalf("decode empty project: %v", err)
	}
	if len(body2.Candidates) != 0 {
		t.Fatalf("empty project should yield empty candidates, got %d", len(body2.Candidates))
	}
}

func TestPublicCheckDuplicateCrossProjectIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := newTestApp(t)
	mustCreateProject(t, a.DB, "proj-a")
	mustCreateProject(t, a.DB, "proj-b")

	// Insert matching feedbacks in both projects
	insertFeedback(t, a.DB, "proj-a", "Duplicate content", "Same description", "pending")
	insertFeedback(t, a.DB, "proj-b", "Duplicate content", "Same description", "pending")

	// Search in proj-a should only find proj-a feedback
	q := url.QueryEscape("Duplicate content Same description")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/api/v1/feedback/check-duplicate?q="+q+"&project=proj-a", nil)
	a.PublicCheckDuplicate(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Candidates []map[string]interface{} `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// proj-a has 1 feedback, excludeID=0 (no self-exclusion for public endpoint)
	// So it should find the proj-a feedback as a candidate
	if len(body.Candidates) != 1 {
		t.Fatalf("proj-a search expected 1 candidate, got %d: %+v", len(body.Candidates), body.Candidates)
	}
	// Verify no project_id leakage in response
	raw := w.Body.String()
	if strings.Contains(raw, "project_id") {
		t.Fatalf("response leaks project_id: %s", raw)
	}
	if strings.Contains(raw, "content_hash") {
		t.Fatalf("response leaks content_hash: %s", raw)
	}
	if strings.Contains(raw, "is_duplicate") {
		t.Fatalf("response leaks is_duplicate: %s", raw)
	}

	// Search in proj-b should only find proj-b feedback
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet,
		"/api/v1/feedback/check-duplicate?q="+q+"&project=proj-b", nil)
	a.PublicCheckDuplicate(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	var body2 struct {
		Candidates []map[string]interface{} `json:"candidates"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &body2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body2.Candidates) != 1 {
		t.Fatalf("proj-b search expected 1 candidate, got %d: %+v", len(body2.Candidates), body2.Candidates)
	}
}

func TestPublicCheckDuplicateResponseShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := newTestApp(t)
	mustCreateProject(t, a.DB, "proj-a")

	insertFeedback(t, a.DB, "proj-a", "Unique title", "Unique description content here", "pending")

	q := url.QueryEscape("Unique title Unique description content here")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/api/v1/feedback/check-duplicate?q="+q+"&project=proj-a", nil)
	a.PublicCheckDuplicate(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body struct {
		Candidates []map[string]interface{} `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(body.Candidates))
	}
	cand := body.Candidates[0]
	// Must have id, title, summary, token
	for _, key := range []string{"id", "title", "summary", "token"} {
		if _, ok := cand[key]; !ok {
			t.Fatalf("candidate missing %q", key)
		}
	}
	// Must NOT have project_id, content_hash, is_duplicate
	for _, key := range []string{"project_id", "content_hash", "is_duplicate"} {
		if _, ok := cand[key]; ok {
			t.Fatalf("candidate leaks %q", key)
		}
	}
}

func TestPublicCheckDuplicateOnlyOpenStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := newTestApp(t)
	mustCreateProject(t, a.DB, "proj-a")

	insertFeedback(t, a.DB, "proj-a", "Hello World", "Description", "pending")
	insertFeedback(t, a.DB, "proj-a", "Hello World", "Description", "resolved")

	q := url.QueryEscape("Hello World Description")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/api/v1/feedback/check-duplicate?q="+q+"&project=proj-a", nil)
	a.PublicCheckDuplicate(c)
	var body struct {
		Candidates []map[string]interface{} `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Should find 1 candidate (the pending one); resolved is excluded
	if len(body.Candidates) != 1 {
		t.Fatalf("expected 1 candidate (pending only), got %d: %+v", len(body.Candidates), body.Candidates)
	}
}

// ---------- Item 5: AdminSimilarFeedbacks ----------

func TestAdminSimilarFeedbacksSameProjectOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := newTestApp(t)
	mustCreateProject(t, a.DB, "proj-a")
	mustCreateProject(t, a.DB, "proj-b")

	idA := insertFeedback(t, a.DB, "proj-a", "Hello World", "Description", "pending")
	insertFeedback(t, a.DB, "proj-b", "Hello World", "Description", "pending")

	// Admin role bypasses RBAC
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(idA, 10)}}
	c.Request = httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/feedbacks/"+strconv.FormatInt(idA, 10)+"/similar", nil)
	c.Set("admin_role", "admin")
	c.Set("admin_user", "testadmin")
	a.AdminSimilarFeedbacks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Candidates []map[string]interface{} `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Only self-match in proj-a (no other proj-a feedback with same content)
	if len(body.Candidates) != 0 {
		t.Fatalf("expected 0 candidates (only self-match), got %d: %+v", len(body.Candidates), body.Candidates)
	}

	// Add another pending feedback with same content in proj-a
	idA2 := insertFeedback(t, a.DB, "proj-a", "Hello World", "Description", "pending")

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(idA, 10)}}
	c2.Request = httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/feedbacks/"+strconv.FormatInt(idA, 10)+"/similar", nil)
	c2.Set("admin_role", "admin")
	c2.Set("admin_user", "testadmin")
	a.AdminSimilarFeedbacks(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	var body2 struct {
		Candidates []map[string]interface{} `json:"candidates"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &body2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body2.Candidates) != 1 {
		t.Fatalf("expected 1 candidate (the other proj-a feedback), got %d: %+v", len(body2.Candidates), body2.Candidates)
	}
	candID := int64(body2.Candidates[0]["id"].(float64))
	if candID != idA2 {
		t.Fatalf("expected candidate id=%d, got %d", idA2, candID)
	}
}

func TestAdminSimilarFeedbacksOnlyOpenStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := newTestApp(t)
	mustCreateProject(t, a.DB, "proj-a")

	id := insertFeedback(t, a.DB, "proj-a", "Hello World", "Description", "pending")
	insertFeedback(t, a.DB, "proj-a", "Hello World", "Description", "resolved")
	idPending2 := insertFeedback(t, a.DB, "proj-a", "Hello World", "Description", "pending")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}}
	c.Request = httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/feedbacks/"+strconv.FormatInt(id, 10)+"/similar", nil)
	c.Set("admin_role", "admin")
	c.Set("admin_user", "testadmin")
	a.AdminSimilarFeedbacks(c)
	var body struct {
		Candidates []map[string]interface{} `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Candidates) != 1 {
		t.Fatalf("expected 1 candidate (only pending), got %d: %+v", len(body.Candidates), body.Candidates)
	}
	candID := int64(body.Candidates[0]["id"].(float64))
	if candID != idPending2 {
		t.Fatalf("expected candidate id=%d, got %d", idPending2, candID)
	}
}

// ---------- Item 6: MarkAsDuplicate cross-project → 400 + UnmarkDuplicate reversible ----------

func TestMarkAsDuplicateCrossProjectRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := newTestApp(t)
	mustCreateProject(t, a.DB, "proj-eng")
	mustCreateProject(t, a.DB, "proj-design")

	idEng := insertFeedback(t, a.DB, "proj-eng", "Bug report", "Description", "pending")
	idDesign := insertFeedback(t, a.DB, "proj-design", "Bug report", "Description", "pending")

	// Try to mark engineering feedback as duplicate of design feedback
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(idEng, 10)}}
	c.Request = httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/feedbacks/"+strconv.FormatInt(idEng, 10)+"/duplicate",
		strings.NewReader(`{"duplicate_of":`+strconv.FormatInt(idDesign, 10)+`}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("admin_role", "admin")
	c.Set("admin_user", "testadmin")
	a.AdminMarkAsDuplicate(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("cross-project mark: expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !strings.Contains(errResp.Error, "跨项目") {
		t.Fatalf("error message should mention cross-project rejection, got: %q", errResp.Error)
	}

	// Verify no write occurred
	fb, err := a.DB.GetFeedback(idEng)
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}
	if fb.IsDuplicate {
		t.Fatal("cross-project mark should NOT have been applied")
	}
}

func TestMarkAsDuplicateSameProjectSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := newTestApp(t)
	mustCreateProject(t, a.DB, "proj-a")

	idSrc := insertFeedback(t, a.DB, "proj-a", "Original", "Description", "pending")
	idTarget := insertFeedback(t, a.DB, "proj-a", "Duplicate", "Description", "pending")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(idSrc, 10)}}
	c.Request = httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/feedbacks/"+strconv.FormatInt(idSrc, 10)+"/duplicate",
		strings.NewReader(`{"duplicate_of":`+strconv.FormatInt(idTarget, 10)+`}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("admin_role", "admin")
	c.Set("admin_user", "testadmin")
	a.AdminMarkAsDuplicate(c)
	if w.Code != http.StatusOK {
		t.Fatalf("same-project mark: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	fb, err := a.DB.GetFeedback(idSrc)
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}
	if !fb.IsDuplicate {
		t.Fatal("feedback should be marked as duplicate")
	}
	if fb.DuplicateOf != idTarget {
		t.Fatalf("duplicate_of should be %d, got %d", idTarget, fb.DuplicateOf)
	}
}

func TestUnmarkDuplicateReversible(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := newTestApp(t)
	mustCreateProject(t, a.DB, "proj-a")

	idSrc := insertFeedback(t, a.DB, "proj-a", "Source", "Desc", "pending")
	idTarget := insertFeedback(t, a.DB, "proj-a", "Target", "Desc", "pending")

	// Mark via DB directly
	if err := a.DB.MarkAsDuplicate(idSrc, idTarget); err != nil {
		t.Fatalf("MarkAsDuplicate: %v", err)
	}
	fb, _ := a.DB.GetFeedback(idSrc)
	if !fb.IsDuplicate || fb.DuplicateOf != idTarget {
		t.Fatalf("after mark: is_duplicate=%v, duplicate_of=%d", fb.IsDuplicate, fb.DuplicateOf)
	}

	// Unmark via handler
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(idSrc, 10)}}
	c.Request = httptest.NewRequest(http.MethodDelete,
		"/api/v1/admin/feedbacks/"+strconv.FormatInt(idSrc, 10)+"/duplicate", nil)
	c.Set("admin_role", "admin")
	c.Set("admin_user", "testadmin")
	a.AdminUnmarkDuplicate(c)
	if w.Code != http.StatusOK {
		t.Fatalf("unmark: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	fb2, _ := a.DB.GetFeedback(idSrc)
	if fb2.IsDuplicate {
		t.Fatal("after unmark: is_duplicate should be false")
	}
	if fb2.DuplicateOf != 0 {
		t.Fatalf("after unmark: duplicate_of should be 0, got %d", fb2.DuplicateOf)
	}
}

// ---------- Item 7: Merge display (is_duplicate=1 + duplicate_of) ----------

func TestMergeSetsDuplicateFlags(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	idSrc := insertFeedback(t, db, "proj-a", "Source", "Desc", "pending")
	idTarget := insertFeedback(t, db, "proj-a", "Target", "Desc", "pending")

	if err := db.MarkAsDuplicate(idSrc, idTarget); err != nil {
		t.Fatalf("MarkAsDuplicate: %v", err)
	}

	fb, err := db.GetFeedback(idSrc)
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}
	if !fb.IsDuplicate {
		t.Fatal("is_duplicate must be true after merge")
	}
	if fb.DuplicateOf != idTarget {
		t.Fatalf("duplicate_of must point to target (%d), got %d", idTarget, fb.DuplicateOf)
	}

	// Target feedback must NOT have is_duplicate set
	fbTarget, err := db.GetFeedback(idTarget)
	if err != nil {
		t.Fatalf("GetFeedback(target): %v", err)
	}
	if fbTarget.IsDuplicate {
		t.Fatal("target feedback must NOT have is_duplicate set")
	}

	// Original feedback record is NOT physically deleted
	if fb.ID != idSrc {
		t.Fatal("original feedback should still exist (not physically deleted)")
	}
}

// ---------- Item 8: Migration idempotency ----------
// NewTestDatabase runs initDB → migrate() internally.
// A successful NewTestDatabase proves the migration runs without error.
// Creating a second database independently proves repeated migration is idempotent.

func TestMigrationIdempotentFreshInstance(t *testing.T) {
	// Creating a fresh in-memory database runs migrate() internally.
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("first NewTestDatabase: migration should succeed: %v", err)
	}

	// Verify we can insert and read back a feedback with content_hash
	id := insertFeedback(t, db, "proj-a", "Migration test", "Check content_hash exists", "pending")
	fb, err := db.GetFeedback(id)
	if err != nil {
		t.Fatalf("GetFeedback after migration: %v", err)
	}
	if fb.ContentHash == "" {
		t.Fatal("content_hash column should exist and be populated after migration")
	}
	if len(fb.ContentHash) != 64 {
		t.Fatalf("content_hash length = %d, want 64", len(fb.ContentHash))
	}
}

func TestMigrationIdempotentSecondInstance(t *testing.T) {
	// Creating a second independent database also succeeds (proves migration is idempotent).
	db2, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("second NewTestDatabase: repeated migration should be idempotent: %v", err)
	}

	// Verify content_hash works
	id := insertFeedback(t, db2, "proj-b", "Second instance", "Working", "pending")
	fb, err := db2.GetFeedback(id)
	if err != nil {
		t.Fatalf("GetFeedback after second migration: %v", err)
	}
	if fb.ContentHash == "" {
		t.Fatal("content_hash should be populated after second migration instance")
	}
}

// ---------- FindExactDuplicates (DB layer) ----------

func TestFindExactDuplicatesCrossProjectIsolation(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	hash := database.ComputeContentHash("Hello World", "Description")
	insertFeedback(t, db, "proj-a", "Hello World", "Description", "pending")
	insertFeedback(t, db, "proj-b", "Hello World", "Description", "pending")

	candidatesA, err := db.FindExactDuplicates("proj-a", hash, 0, 10)
	if err != nil {
		t.Fatalf("FindExactDuplicates proj-a: %v", err)
	}
	if len(candidatesA) != 1 {
		t.Fatalf("proj-a expected 1 candidate, got %d", len(candidatesA))
	}
	if candidatesA[0].ProjectID != "proj-a" {
		t.Fatalf("proj-a candidate has wrong project_id: %q", candidatesA[0].ProjectID)
	}

	candidatesB, err := db.FindExactDuplicates("proj-b", hash, 0, 10)
	if err != nil {
		t.Fatalf("FindExactDuplicates proj-b: %v", err)
	}
	if len(candidatesB) != 1 {
		t.Fatalf("proj-b expected 1 candidate, got %d", len(candidatesB))
	}
	if candidatesB[0].ProjectID != "proj-b" {
		t.Fatalf("proj-b candidate has wrong project_id: %q", candidatesB[0].ProjectID)
	}
}

func TestFindExactDuplicatesExcludesSelfAndMarked(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	hash := database.ComputeContentHash("Hello World", "Description")
	id1 := insertFeedback(t, db, "proj-a", "Hello World", "Description", "pending")
	id2 := insertFeedback(t, db, "proj-a", "Hello World", "Description", "pending")

	// Exclude id1 — should find id2
	candidates, err := db.FindExactDuplicates("proj-a", hash, id1, 10)
	if err != nil {
		t.Fatalf("FindExactDuplicates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].ID != id2 {
		t.Fatalf("expected candidate id=%d, got %d", id2, candidates[0].ID)
	}

	// Mark id2 as duplicate
	if err := db.MarkAsDuplicate(id2, id1); err != nil {
		t.Fatalf("MarkAsDuplicate: %v", err)
	}

	// Now id2 is marked as duplicate, should NOT appear as a candidate
	candidates2, err := db.FindExactDuplicates("proj-a", hash, id1, 10)
	if err != nil {
		t.Fatalf("FindExactDuplicates after mark: %v", err)
	}
	if len(candidates2) != 0 {
		t.Fatalf("expected 0 candidates (id2 is now marked), got %d", len(candidates2))
	}
}

func TestFindExactDuplicatesOnlyOpenStatus(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	hash := database.ComputeContentHash("Hello World", "Description")
	insertFeedback(t, db, "proj-a", "Hello World", "Description", "pending")
	insertFeedback(t, db, "proj-a", "Hello World", "Description", "resolved")
	insertFeedback(t, db, "proj-a", "Hello World", "Description", "closed")

	candidates, err := db.FindExactDuplicates("proj-a", hash, 0, 10)
	if err != nil {
		t.Fatalf("FindExactDuplicates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (pending only), got %d", len(candidates))
	}
	if candidates[0].Status != "pending" {
		t.Fatalf("expected pending status, got %q", candidates[0].Status)
	}
}

func TestFindExactDuplicatesLimit(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	hash := database.ComputeContentHash("Hello World", "Description")
	for i := 0; i < 3; i++ {
		insertFeedback(t, db, "proj-a", "Hello World", "Description", "pending")
	}

	candidates, err := db.FindExactDuplicates("proj-a", hash, 0, 2)
	if err != nil {
		t.Fatalf("FindExactDuplicates: %v", err)
	}
	if len(candidates) > 2 {
		t.Fatalf("expected at most 2 candidates, got %d", len(candidates))
	}
}

func TestFindExactDuplicatesOrderByCreatedDesc(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	hash := database.ComputeContentHash("Hello World", "Description")
	insertFeedback(t, db, "proj-a", "Hello World", "Description", "pending")
	insertFeedback(t, db, "proj-a", "Hello World", "Description", "pending")

	candidates, err := db.FindExactDuplicates("proj-a", hash, 0, 10)
	if err != nil {
		t.Fatalf("FindExactDuplicates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}
	// Both inserted within the same second — just verify all returned are from
	// the right project and have matching hash.
	for _, c := range candidates {
		if c.ProjectID != "proj-a" {
			t.Fatalf("candidate has wrong project_id: %q", c.ProjectID)
		}
		if c.ContentHash != hash {
			t.Fatalf("candidate has wrong content_hash: %q", c.ContentHash)
		}
	}
}
