package app

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"feedshit/internal/config"
	"feedshit/internal/database"
	"feedshit/internal/email"
	"feedshit/internal/middleware"
)

// writeTestPNG encodes a solid-color PNG of the given size into dir/fname and
// returns the full path. Used to exercise the real thumbnail pipeline.
func writeTestPNG(t *testing.T, dir, fname string, w, h int) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	full := filepath.Join(dir, fname)
	if err := os.WriteFile(full, buf.Bytes(), 0644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return full
}

func newImageTestApp(t *testing.T) *App {
	t.Helper()
	tmp := t.TempDir()
	cfg := &config.Config{
		APITokenDefaultRateLimit: 60,
		BackupRetentionDays:      30,
		BaseURL:                  "http://localhost:8080",
		DataDir:                  tmp,
		UploadDir:                filepath.Join(tmp, "uploads"),
	}
	if err := os.MkdirAll(cfg.UploadDir, 0755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	sm := middleware.NewSessionManager()
	rl := middleware.NewRateLimiter(10)
	mailer := email.NewMailer(db, cfg.BaseURL)
	app := New(cfg, db, sm, rl, mailer)
	return app
}

func TestTrackThumbPipeline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newImageTestApp(t)
	_ = app // silence if unused in some build configs

	// Seed a feedback with two attachments: a PNG and a .txt (non-raster).
	up := app.Cfg.UploadDir
	pngPath := writeTestPNG(t, filepath.Join(up, "test"), "shot.png", 400, 300)
	_ = pngPath
	txtPath := filepath.Join(up, "test", "notes.txt")
	if err := os.WriteFile(txtPath, []byte("hello"), 0644); err != nil {
		t.Fatalf("write txt: %v", err)
	}
	f := &database.Feedback{
		ProjectID:     "proj-x",
		Title:         "thumb test",
		Description:   "d",
		Status:        "pending",
		TrackingToken: "tok123",
		FilePaths:     `["uploads/test/shot.png","uploads/test/notes.txt"]`,
	}
	id, err := app.DB.InsertFeedback(f)
	if err != nil {
		t.Fatalf("InsertFeedback: %v", err)
	}
	_ = id

	// 1) Valid raster image → 200, image content-type, body is a (resized) image.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/track/thumb?token=tok123&note=0&i=0", nil)
	app.PublicServeTrackFileThumb(c)
	if w.Code != http.StatusOK {
		t.Fatalf("raster thumb: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "image/") {
		t.Fatalf("raster thumb: expected image content-type, got %q", w.Header().Get("Content-Type"))
	}
	// Cache-Control immutable for thumbnails.
	if !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("raster thumb: expected immutable cache-control, got %q", w.Header().Get("Cache-Control"))
	}

	// 2) Non-raster file (.txt) → 404 (client falls back to download).
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/v1/track/thumb?token=tok123&note=0&i=1", nil)
	app.PublicServeTrackFileThumb(c2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("non-raster thumb: expected 404, got %d", w2.Code)
	}

	// 3) Out-of-range index → 404.
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Request = httptest.NewRequest(http.MethodGet, "/api/v1/track/thumb?token=tok123&note=0&i=99", nil)
	app.PublicServeTrackFileThumb(c3)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("out-of-range thumb: expected 404, got %d", w3.Code)
	}

	// 4) Missing token → 400.
	w4 := httptest.NewRecorder()
	c4, _ := gin.CreateTestContext(w4)
	c4.Request = httptest.NewRequest(http.MethodGet, "/api/v1/track/thumb?note=0&i=0", nil)
	app.PublicServeTrackFileThumb(c4)
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("missing token: expected 400, got %d", w4.Code)
	}
}

func TestTrackThumbOversizeGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newImageTestApp(t)

	up := app.Cfg.UploadDir
	// 8100px wide exceeds maxDecodeSide (8000) → must fall back to 404.
	writeTestPNG(t, filepath.Join(up, "big"), "huge.png", 8100, 10)
	f := &database.Feedback{
		ProjectID:     "proj-x",
		Title:         "oversize",
		Status:        "pending",
		TrackingToken: "tokBig",
		FilePaths:     `["uploads/big/huge.png"]`,
	}
	if _, err := app.DB.InsertFeedback(f); err != nil {
		t.Fatalf("InsertFeedback: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/track/thumb?token=tokBig&note=0&i=0", nil)
	app.PublicServeTrackFileThumb(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("oversize thumb: expected 404, got %d", w.Code)
	}
}

func TestAdminThumbPipeline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newImageTestApp(t)

	up := app.Cfg.UploadDir
	writeTestPNG(t, filepath.Join(up, "test"), "shot.png", 400, 300)
	f := &database.Feedback{
		ProjectID:     "proj-x",
		Title:         "admin thumb",
		Status:        "pending",
		TrackingToken: "tokAdm",
		FilePaths:     `["uploads/test/shot.png"]`,
	}
	id, err := app.DB.InsertFeedback(f)
	if err != nil {
		t.Fatalf("InsertFeedback: %v", err)
	}

	// Admin session forged via context values (mirrors AuthMiddleware admin path).
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/feedbacks/"+strconv.FormatInt(id, 10)+"/thumb?note=0&i=0", nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}}
	c.Set("admin_role", "admin")
	app.AdminServeFeedbackThumb(c)
	if w.Code != http.StatusOK {
		t.Fatalf("admin thumb: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "image/") {
		t.Fatalf("admin thumb: expected image content-type, got %q", w.Header().Get("Content-Type"))
	}
}

// TestPublicRoadmapDescriptionAndTotal verifies the roadmap board API now
// returns item descriptions (for card summary + detail modal) and a total
// count (for pagination).
func TestPublicRoadmapDescriptionAndTotal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)
	longDesc := "这是一段用于验证路线图卡片摘要与详情弹窗的较长描述，应当随 RoadmapItem 一并返回。"
	f1 := &database.Feedback{ProjectID: "default", Title: "功能 A", Description: longDesc, Category: "功能", RoadmapStatus: "planning"}
	id1, err := app.DB.InsertFeedback(f1)
	if err != nil {
		t.Fatalf("InsertFeedback f1: %v", err)
	}
	if err := app.DB.SetRoadmap(id1, true, "planning"); err != nil {
		t.Fatalf("SetRoadmap f1: %v", err)
	}
	f2 := &database.Feedback{ProjectID: "default", Title: "功能 B", Description: "已发布的功能", Category: "功能", RoadmapStatus: "released"}
	id2, err := app.DB.InsertFeedback(f2)
	if err != nil {
		t.Fatalf("InsertFeedback f2: %v", err)
	}
	if err := app.DB.SetRoadmap(id2, true, "released"); err != nil {
		t.Fatalf("SetRoadmap f2: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/roadmap?slug=default", nil)
	app.PublicRoadmap(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []struct {
			ID            int64  `json:"id"`
			Description   string `json:"description"`
			RoadmapStatus string `json:"roadmap_status"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total < 2 {
		t.Fatalf("expected total >= 2, got %d", resp.Total)
	}
	found := false
	for _, it := range resp.Items {
		if it.Description == longDesc {
			found = true
		}
	}
	if !found {
		t.Fatalf("description not returned in items: %s", w.Body.String())
	}
}
