package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"feedshit/internal/security"
)

// postJSON runs a handler with a JSON body and returns the recorder.
func postJSON(app *App, path, body string, fn func(c *gin.Context)) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	fn(c)
	return w
}

func TestSetupStatusExposesSystemInfo(t *testing.T) {
	app := newTestApp(t)
	// Production populates these via config.LoadConfig; mirror that here.
	app.Cfg.DataDir = "./data"
	app.Cfg.DBPath = "./data/feedbacks.db"
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil)
	app.SetupStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("SetupStatus: expected 200, got %d", w.Code)
	}
	var d map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if d["version"] == "" {
		t.Error("SetupStatus: version missing")
	}
	if d["master_key_source"] != "env" && d["master_key_source"] != "file" && d["master_key_source"] != "generated" {
		t.Errorf("SetupStatus: unexpected master_key_source %q", d["master_key_source"])
	}
	// Security: sensitive internal paths and runtime details must NOT be exposed
	// on this unauthenticated endpoint.
	for _, field := range []string{"go_version", "data_dir", "db_path"} {
		if _, exists := d[field]; exists {
			t.Errorf("SetupStatus: sensitive field %q must not be exposed publicly", field)
		}
	}
}

func TestDoSetupPersistsSMTP(t *testing.T) {
	// Initialize the master key (production does this in main.go) so smtp_pass
	// can be encrypted at rest.
	if err := security.InitWithKey([]byte("0123456789abcdef0123456789abcdef")); err != nil {
		t.Fatalf("init security: %v", err)
	}
	app := newTestApp(t)
	body := `{
		"admin_username":"admin",
		"admin_password":"Abcd1234",
		"smtp_host":"smtp.example.com",
		"smtp_port":587,
		"smtp_user":"user@example.com",
		"smtp_pass":"secret-smtp",
		"smtp_from":"noreply@example.com",
		"smtp_to":"admin@example.com",
		"notify_enable":true
	}`
	w := postJSON(app, "/api/v1/setup", body, app.DoSetup)
	if w.Code != http.StatusOK {
		t.Fatalf("DoSetup: expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	if app.DB.GetConfig("setup_complete") != "true" {
		t.Error("DoSetup: setup_complete not set")
	}
	if got := app.DB.GetConfig("smtp_host"); got != "smtp.example.com" {
		t.Errorf("DoSetup: smtp_host not persisted, got %q", got)
	}
	if got := app.DB.GetConfig("smtp_port"); got != "587" {
		t.Errorf("DoSetup: smtp_port not persisted, got %q", got)
	}
	if got := app.DB.GetConfig("notify_enable"); got != "true" {
		t.Errorf("DoSetup: notify_enable not persisted, got %q", got)
	}
	// smtp_pass must be persisted AND round-trip through the encrypted store.
	if got := app.DB.GetConfig("smtp_pass"); got != "secret-smtp" {
		t.Errorf("DoSetup: smtp_pass not persisted/decrypted, got %q", got)
	}
}
