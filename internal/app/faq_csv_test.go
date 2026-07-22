package app

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestAdminExportAndImportFAQs exercises the C3-2 FAQ CSV endpoints:
// export streams question/answer/sort_order/is_active; import preview reports
// the column mapping; real import creates new rows and skips duplicates.
func TestAdminExportAndImportFAQs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)
	slug := "csvproj"

	// Seed two FAQs directly (no project existence check needed for CreateFAQ).
	if _, err := app.DB.CreateFAQ(slug, "如何重置密码", "**重启** 即可", 1, true); err != nil {
		t.Fatalf("seed FAQ 1: %v", err)
	}
	if _, err := app.DB.CreateFAQ(slug, "如何导出", "见文档", 2, false); err != nil {
		t.Fatalf("seed FAQ 2: %v", err)
	}

	// --- export ---
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: slug}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/projects/"+slug+"/faqs/export", nil)
	app.AdminExportFAQs(c)
	if w.Code != http.StatusOK {
		t.Fatalf("export status %d body=%s", w.Code, w.Body.String())
	}
	exported := w.Body.String()
	if !strings.Contains(exported, "问题") || !strings.Contains(exported, "如何重置密码") {
		t.Fatalf("export missing expected columns/rows: %s", exported)
	}
	if !strings.Contains(exported, "true") || !strings.Contains(exported, "false") {
		t.Fatalf("export missing is_active values: %s", exported)
	}

	// --- import preview ---
	csvContent := "问题,答案,排序,启用\n如何导入,上传CSV即可,3,true\n如何重置密码,重复问题应跳过,1,true\n"
	var pbuf bytes.Buffer
	pmw := multipart.NewWriter(&pbuf)
	ppart, _ := pmw.CreateFormFile("file", "faqs.csv")
	ppart.Write([]byte(csvContent))
	pmw.Close()
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Params = gin.Params{{Key: "id", Value: slug}}
	c2.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/projects/"+slug+"/faqs/import?preview=1", &pbuf)
	c2.Request.Header.Set("Content-Type", pmw.FormDataContentType())
	app.AdminImportFAQs(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("preview status %d body=%s", w2.Code, w2.Body.String())
	}
	var pv struct {
		HasQuestion bool              `json:"has_question"`
		Mapped      map[string]string `json:"mapped"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &pv); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if !pv.HasQuestion {
		t.Fatalf("preview should report has_question=true; mapped=%v", pv.Mapped)
	}
	if pv.Mapped["问题"] != "question" {
		t.Fatalf("expected 问题->question mapping, got %q", pv.Mapped["问题"])
	}

	// --- real import (multipart) ---
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "faqs.csv")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	part.Write([]byte(csvContent))
	mw.Close()

	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Params = gin.Params{{Key: "id", Value: slug}}
	c3.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/projects/"+slug+"/faqs/import", &buf)
	c3.Request.Header.Set("Content-Type", mw.FormDataContentType())
	app.AdminImportFAQs(c3)
	if w3.Code != http.StatusOK {
		t.Fatalf("import status %d body=%s", w3.Code, w3.Body.String())
	}
	var imp struct {
		Imported int `json:"imported"`
		Skipped  int `json:"skipped"`
	}
	if err := json.Unmarshal(w3.Body.Bytes(), &imp); err != nil {
		t.Fatalf("decode import: %v", err)
	}
	// "如何导入" is new -> imported; "如何重置密码" duplicates a seeded row -> skipped.
	if imp.Imported != 1 {
		t.Fatalf("expected 1 imported, got %d", imp.Imported)
	}
	if imp.Skipped != 1 {
		t.Fatalf("expected 1 skipped (duplicate question), got %d", imp.Skipped)
	}
}
