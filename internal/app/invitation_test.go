package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestInvitationDatabaseLayer(t *testing.T) {
	app := newTestApp(t)

	// Create invitation
	inv, err := app.DB.CreateInvitation("editor", []string{"project-a", "project-b"}, 5, "admin", 7)
	if err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}
	if inv.Token == "" {
		t.Fatal("token should not be empty")
	}
	if inv.Role != "editor" {
		t.Fatalf("role = %q, want editor", inv.Role)
	}
	if inv.MaxUses != 5 {
		t.Fatalf("max_uses = %d, want 5", inv.MaxUses)
	}

	// Validate invitation
	validated, err := app.DB.ValidateInvitation(inv.Token)
	if err != nil {
		t.Fatalf("ValidateInvitation failed: %v", err)
	}
	if validated == nil {
		t.Fatal("expected valid invitation, got nil")
	}
	if validated.Token != inv.Token {
		t.Fatalf("token mismatch")
	}

	// List invitations
	list, err := app.DB.ListInvitations()
	if err != nil {
		t.Fatalf("ListInvitations failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 invitation, got %d", len(list))
	}

	// Use invitation
	if err := app.DB.UseInvitation(inv.Token); err != nil {
		t.Fatalf("UseInvitation failed: %v", err)
	}
	if err := app.DB.UseInvitation(inv.Token); err != nil {
		t.Fatalf("UseInvitation (2nd) failed: %v", err)
	}

	// Validate again — should still work (5 max uses, used 2)
	validated2, err := app.DB.ValidateInvitation(inv.Token)
	if err != nil {
		t.Fatalf("ValidateInvitation after use failed: %v", err)
	}
	if validated2 == nil {
		t.Fatal("expected valid after 2/5 uses")
	}
}

func TestInvitationExhausted(t *testing.T) {
	app := newTestApp(t)

	inv, err := app.DB.CreateInvitation("viewer", nil, 2, "admin", 0)
	if err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}

	// Use twice
	app.DB.UseInvitation(inv.Token)
	app.DB.UseInvitation(inv.Token)

	// Third use should exhaust
	_, err = app.DB.ValidateInvitation(inv.Token)
	if err == nil {
		t.Fatal("expected error for exhausted invitation")
	}
}

func TestInvitationExpired(t *testing.T) {
	app := newTestApp(t)

	// Create with 0 days = no expiry
	inv, err := app.DB.CreateInvitation("editor", nil, 1, "admin", 0)
	if err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}

	// Should be valid
	v, err := app.DB.ValidateInvitation(inv.Token)
	if err != nil || v == nil {
		t.Fatalf("expected valid invitation: err=%v", err)
	}
}

func TestPublicRegisterPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)

	inv, err := app.DB.CreateInvitation("editor", nil, 1, "admin", 7)
	if err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{{Key: "token", Value: inv.Token}}
	app.PublicRegisterPage(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "加入团队") {
		t.Fatal("expected registration page content")
	}
}

func TestPublicRegisterPageInvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{{Key: "token", Value: "invalidtoken123"}}
	app.PublicRegisterPage(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "无效") {
		t.Fatal("expected 'invalid' message for bad token")
	}
}

func TestPublicRegisterSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)

	inv, err := app.DB.CreateInvitation("editor", []string{"default"}, 1, "admin", 7)
	if err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{{Key: "token", Value: inv.Token}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/invite/"+inv.Token+"/register",
		strings.NewReader(`{"username":"newuser","password":"StrongPass1"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	app.PublicRegister(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if resp.Message != "注册成功" {
		t.Fatalf("expected 注册成功, got %q", resp.Message)
	}

	// Verify admin was created
	admin, err := app.DB.GetAdminByUsername("newuser")
	if err != nil || admin == nil {
		t.Fatalf("admin not created: %v", err)
	}
	if admin.Role != "editor" {
		t.Fatalf("role = %q, want editor", admin.Role)
	}
}

func TestPublicRegisterValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := newTestApp(t)

	inv, err := app.DB.CreateInvitation("editor", nil, 5, "admin", 7)
	if err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantErr    string
	}{
		{"empty fields", `{"username":"","password":""}`, http.StatusBadRequest, "不能为空"},
		{"short username", `{"username":"ab","password":"StrongPass1"}`, http.StatusBadRequest, "3-32"},
		{"weak password", `{"username":"validuser","password":"short"}`, http.StatusBadRequest, "至少"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = []gin.Param{{Key: "token", Value: inv.Token}}
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/invite/"+inv.Token+"/register",
				strings.NewReader(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			app.PublicRegister(c)

			if w.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d body=%s", tt.wantStatus, w.Code, w.Body.String())
			}
			var resp struct {
				Error string `json:"error"`
			}
			json.Unmarshal(w.Body.Bytes(), &resp)
			if !strings.Contains(resp.Error, tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, resp.Error)
			}
		})
	}
}
