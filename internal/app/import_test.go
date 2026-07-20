package app

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"feedshit/internal/database"
)

// testCtx creates a gin context with admin session and returns both context and recorder.
func testCtx(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("admin_user", "testadmin")
	c.Set("admin_role", "admin")
	return c, w
}

// seedDefaultProject inserts a default project if it doesn't exist.
func seedDefaultProject(t *testing.T, app *App) {
	t.Helper()
	proj, err := app.DB.GetProjectBySlug("default")
	if err != nil || proj == nil {
		if _, err := app.DB.CreateProject(&database.Project{Name: "默认项目", Slug: "default", Description: "系统默认项目", IsActive: true}); err != nil {
			t.Fatalf("CreateProject failed: %v", err)
		}
	}
}

func TestAdminImportCSVSuccess(t *testing.T) {
	c, w := testCtx(t)
	app := newTestApp(t)
	seedDefaultProject(t, app)

	csvContent := "标题,描述,状态\n测试反馈,这是一个测试反馈,pending\n第二个反馈,另一个描述,pending\n"
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "test.csv")
	io.Copy(part, strings.NewReader(csvContent))
	writer.Close()

	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/import/csv", body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	app.AdminImportCSV(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Imported int `json:"imported"`
		Total    int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if resp.Imported != 2 {
		t.Fatalf("expected 2 imported, got %d ; body=%s", resp.Imported, w.Body.String())
	}
	if resp.Total != 2 {
		t.Fatalf("expected 2 total, got %d ; body=%s", resp.Total, w.Body.String())
	}
}

func TestAdminImportCSVWithChineseHeaders(t *testing.T) {
	c, w := testCtx(t)
	app := newTestApp(t)
	seedDefaultProject(t, app)

	csvContent := "标题,描述,优先级\n中文反馈,带中文的描述,high\n"
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "test.csv")
	io.Copy(part, strings.NewReader(csvContent))
	writer.Close()

	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/import/csv", body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	app.AdminImportCSV(c)

	var resp struct {
		Imported int `json:"imported"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Imported != 1 {
		t.Fatalf("expected 1 imported, got %d", resp.Imported)
	}
}

func TestAdminImportCSVEmptyTitle(t *testing.T) {
	c, w := testCtx(t)
	app := newTestApp(t)
	seedDefaultProject(t, app)

	csvContent := "标题,描述\n,空标题行\n"
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "test.csv")
	io.Copy(part, strings.NewReader(csvContent))
	writer.Close()

	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/import/csv", body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	app.AdminImportCSV(c)

	var resp struct {
		Imported int           `json:"imported"`
		Errors   []interface{} `json:"errors"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Imported != 0 {
		t.Fatalf("expected 0 imported for empty titles, got %d", resp.Imported)
	}
	if len(resp.Errors) == 0 {
		t.Fatal("expected errors for empty title rows")
	}
}

func TestAdminImportJSONSuccess(t *testing.T) {
	c, w := testCtx(t)
	app := newTestApp(t)
	seedDefaultProject(t, app)

	jsonBody := `[
		{"title":"JSON反馈1","description":"来自JSON的描述","priority":"high"},
		{"title":"JSON反馈2","description":"另一个JSON","priority":"low"}
	]`

	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/import/json", strings.NewReader(jsonBody))
	c.Request.Header.Set("Content-Type", "application/json")
	app.AdminImportJSON(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Imported int `json:"imported"`
		Total    int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if resp.Imported != 2 {
		t.Fatalf("expected 2 imported, got %d", resp.Imported)
	}
	if resp.Total != 2 {
		t.Fatalf("expected 2 total, got %d", resp.Total)
	}
}

func TestAdminImportJSONInvalid(t *testing.T) {
	c, w := testCtx(t)
	app := newTestApp(t)
	seedDefaultProject(t, app)

	jsonBody := `[
		{"title":"测试","project_id":"nonexistent"}
	]`

	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/import/json", strings.NewReader(jsonBody))
	c.Request.Header.Set("Content-Type", "application/json")
	app.AdminImportJSON(c)

	var resp struct {
		Imported int           `json:"imported"`
		Errors   []interface{} `json:"errors"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Imported != 0 {
		t.Fatalf("expected 0 imported for invalid project, got %d", resp.Imported)
	}
}
