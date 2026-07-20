package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"feedshit/internal/database"
	"feedshit/internal/middleware"
)

// defaultInviteExpiryDays is applied when a caller omits expires_in_days.
// An explicit 0 still means "never expire".
const defaultInviteExpiryDays = 7

// AdminCreateInvitation generates an invitation link for new team members.
// Route: POST /api/v1/admin/invitations (admin only)
func (a *App) AdminCreateInvitation(c *gin.Context) {
	var req struct {
		Role          string   `json:"role"`
		ProjectIDs    []string `json:"project_ids"`
		MaxUses       int      `json:"max_uses"`
		ExpiresInDays *int     `json:"expires_in_days"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.Role == "" {
		req.Role = "editor"
	}
	if req.MaxUses <= 0 {
		req.MaxUses = 1
	}

	// Expiry policy: omit → default (7d); explicit 0 → never expire; positive → N days.
	// A leaked invitation link must not stay valid forever, so we refuse to create
	// an open-ended link unless the admin consciously opts in with 0.
	expiryDays := defaultInviteExpiryDays
	if req.ExpiresInDays != nil {
		if *req.ExpiresInDays == 0 {
			expiryDays = 0
		} else {
			expiryDays = *req.ExpiresInDays
		}
	}

	user, _ := c.Get("admin_user")
	username := fmt.Sprintf("%v", user)

	inv, err := a.DB.CreateInvitation(req.Role, req.ProjectIDs, req.MaxUses, username, expiryDays)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建邀请失败"})
		return
	}

	inviteURL := fmt.Sprintf("%s/invite/%s", a.Cfg.BaseURL, inv.Token)

	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("create_invitation", fmt.Sprintf("创建邀请链接 (角色:%s, 上限:%d)", req.Role, req.MaxUses), username, clientIP)

	c.JSON(http.StatusCreated, gin.H{
		"token":   inv.Token,
		"url":     inviteURL,
		"role":    inv.Role,
		"max_uses": inv.MaxUses,
	})
}

// AdminListInvitations lists all invitation tokens.
// Route: GET /api/v1/admin/invitations (admin only)
func (a *App) AdminListInvitations(c *gin.Context) {
	list, err := a.DB.ListInvitations()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	type respItem struct {
		ID         int64  `json:"id"`
		Token      string `json:"token"`
		Role       string `json:"role"`
		ProjectIDs string `json:"project_ids"`
		MaxUses    int    `json:"max_uses"`
		UsedCount  int    `json:"used_count"`
		CreatedBy  string `json:"created_by"`
		CreatedAt  int64  `json:"created_at"`
		ExpiresAt  int64  `json:"expires_at"`
		URL        string `json:"url"`
		Expired    bool   `json:"expired"`
	}

	var resp []respItem
	for _, inv := range list {
		resp = append(resp, respItem{
			ID:         inv.ID,
			Token:      inv.Token,
			Role:       inv.Role,
			ProjectIDs: inv.ProjectIDs,
			MaxUses:    inv.MaxUses,
			UsedCount:  inv.UsedCount,
			CreatedBy:  inv.CreatedBy,
			CreatedAt:  inv.CreatedAt,
			ExpiresAt:  inv.ExpiresAt,
			URL:        fmt.Sprintf("%s/invite/%s", a.Cfg.BaseURL, inv.Token),
			Expired:    (inv.MaxUses > 0 && inv.UsedCount >= inv.MaxUses) || (inv.ExpiresAt > 0 && time.Now().Unix() > inv.ExpiresAt),
		})
	}
	if resp == nil {
		resp = []respItem{}
	}
	c.JSON(http.StatusOK, gin.H{"invitations": resp})
}

// PublicRegisterPage renders the registration form for an invitation.
func (a *App) PublicRegisterPage(c *gin.Context) {
	token := c.Param("token")

	// Validate the token
	_, err := a.DB.ValidateInvitation(token)
	if err != nil {
		c.String(http.StatusOK, `<html><body style="font-family:sans-serif;padding:40px;text-align:center"><h2>邀请链接无效或已过期</h2><p>请联系管理员获取新的邀请链接。</p></body></html>`)
		return
	}

	html := a.RegisterHTML
	html = strings.ReplaceAll(html, "INVITE_TOKEN_PLACEHOLDER", token)
	// Apply CSP nonce if available
	if nonce, exists := c.Get("csp_nonce"); exists {
		if n, ok := nonce.(string); ok {
			html = strings.ReplaceAll(html, "__NONCE__", n)
		}
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

// PublicRegister handles the registration form submission.

func (a *App) PublicRegister(c *gin.Context) {
	token := c.Param("token")

	// Validate token
	inv, err := a.DB.ValidateInvitation(token)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名和密码不能为空"})
		return
	}
	if len(req.Username) < 3 || len(req.Username) > 32 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名须为 3-32 位"})
		return
	}
	if err := validatePasswordStrength(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check if username exists
	if existing, _ := a.DB.GetAdminByUsername(req.Username); existing != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "用户名已存在"})
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}

	// Parse project IDs
	var projectIDs []string
	if inv.ProjectIDs != "" && inv.ProjectIDs != "[]" {
		json.Unmarshal([]byte(inv.ProjectIDs), &projectIDs)
	}

	// Create admin account
	adminID, err := a.DB.CreateAdmin(req.Username, hash, inv.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
		return
	}

	// Grant project permissions
	if len(projectIDs) > 0 {
		var grants []database.MemberGrant
		for _, slug := range projectIDs {
			grants = append(grants, database.MemberGrant{
				ProjectSlug: slug,
				CategoryKey: "*",
				Role:        inv.Role,
			})
		}
		a.DB.SetMemberGrants(adminID, grants)
	}

	// Increment usage
	a.DB.UseInvitation(token)

	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("register_from_invite", fmt.Sprintf("通过邀请链接注册管理员 %s", req.Username), req.Username, clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "注册成功", "login_url": "/admin/"})
}
