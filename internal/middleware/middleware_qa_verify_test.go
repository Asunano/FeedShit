package middleware

// Independent QA verification tests (Phase 0+1) — authored by QA. RequireRole is
// a PRD-required security-critical pure function that the engineer's self-tests
// did not cover.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// Point #8: RequireRole enforces the admin>manager>editor>viewer hierarchy.
func TestQAVerifyRequireRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		userRole string
		minRole  string
		wantPass bool
	}{
		{"admin", "viewer", true},
		{"admin", "manager", true},
		{"admin", "admin", true},
		{"manager", "editor", true},
		{"manager", "admin", false},
		{"editor", "manager", false},
		{"editor", "editor", true},
		{"viewer", "editor", false},
		{"viewer", "viewer", true},
		{"", "viewer", false},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		ctx.Set("admin_role", c.userRole)
		RequireRole(c.minRole)(ctx)
		passed := !ctx.IsAborted() && w.Code != http.StatusForbidden
		if passed != c.wantPass {
			t.Fatalf("RequireRole(user=%q, min=%q): pass=%v want %v", c.userRole, c.minRole, passed, c.wantPass)
		}
	}
}
