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

// AdminCreateInvitation generates an invitation link for new team members.
// Route: POST /api/v1/admin/invitations (admin only)
func (a *App) AdminCreateInvitation(c *gin.Context) {
	var req struct {
		Role          string   `json:"role"`
		ProjectIDs    []string `json:"project_ids"`
		MaxUses       int      `json:"max_uses"`
		ExpiresInDays int      `json:"expires_in_days"`
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

	user, _ := c.Get("admin_user")
	username := fmt.Sprintf("%v", user)

	inv, err := a.DB.CreateInvitation(req.Role, req.ProjectIDs, req.MaxUses, username, req.ExpiresInDays)
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

	html := registerPageHTML
	html = strings.ReplaceAll(html, "INVITE_TOKEN_PLACEHOLDER", token)
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

// registerPageHTML is the HTML for the invitation registration page.
var registerPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>注册 - FeedShit</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,sans-serif;background:#f5f5f5;display:flex;align-items:center;justify-content:center;min-height:100vh;padding:20px}
.card{background:#fff;border-radius:8px;padding:32px;width:100%;max-width:400px;box-shadow:0 2px 12px rgba(0,0,0,.08)}
h1{font-size:1.2rem;margin-bottom:4px;color:#333}
p{font-size:.85rem;color:#888;margin-bottom:20px}
.field{margin-bottom:16px}
label{display:block;font-size:.8rem;font-weight:600;margin-bottom:4px;color:#555}
input{width:100%;padding:10px 12px;border:1px solid #ddd;border-radius:4px;font-size:.9rem}
input:focus{outline:none;border-color:#e53e3e;box-shadow:0 0 0 2px rgba(229,62,62,.1)}
.btn{width:100%;padding:10px;background:#e53e3e;color:#fff;border:none;border-radius:4px;font-size:.9rem;cursor:pointer}
.btn:hover{background:#c53030}
.btn:disabled{opacity:.6;cursor:not-allowed}
.error{color:#c00;font-size:.8rem;margin-top:4px;display:none}
.success{text-align:center;padding:20px}
.success h2{color:#2d6;font-size:1.1rem;margin-bottom:8px}
</style>
</head>
<body>
<div class="card">
  <h1>加入团队</h1>
  <p>您已被邀请成为团队成员，请设置您的账号信息。</p>
  <div class="field">
    <label for="username">用户名</label>
    <input type="text" id="username" placeholder="3-32 位字母数字" autocomplete="username">
    <div class="error" id="usernameError"></div>
  </div>
  <div class="field">
    <label for="password">密码</label>
    <input type="password" id="password" placeholder="至少 8 位，包含大小写字母和数字" autocomplete="new-password">
    <div class="error" id="passwordError"></div>
  </div>
  <button class="btn" id="registerBtn" onclick="doRegister()">注册</button>
  <div class="error" id="formError" style="margin-top:12px"></div>
  <div id="successMsg" style="display:none">
    <div class="success">
      <h2>✅ 注册成功</h2>
      <p>您可以 <a href="/admin/" style="color:#e53e3e">登录后台</a> 开始使用了。</p>
    </div>
  </div>
</div>
<script>
var TOKEN = 'INVITE_TOKEN_PLACEHOLDER';
async function doRegister() {
  var username = document.getElementById('username').value.trim();
  var password = document.getElementById('password').value;
  document.getElementById('formError').style.display = 'none';
  if (username.length < 3) { document.getElementById('usernameError').textContent='用户名至少 3 位'; document.getElementById('usernameError').style.display=''; return; }
  if (password.length < 8) { document.getElementById('passwordError').textContent='密码至少 8 位'; document.getElementById('passwordError').style.display=''; return; }
  document.getElementById('registerBtn').disabled = true;
  document.getElementById('registerBtn').textContent = '注册中...';
  try {
    var resp = await fetch('/api/v1/invite/' + TOKEN + '/register', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:username,password:password})});
    var d = await resp.json();
    if (d.error) { document.getElementById('formError').textContent=d.error; document.getElementById('formError').style.display=''; document.getElementById('registerBtn').disabled=false; document.getElementById('registerBtn').textContent='注册'; return; }
    document.getElementById('registerBtn').style.display='none';
    document.querySelector('h1').style.display='none';
    document.querySelector('p').style.display='none';
    document.getElementById('username').parentNode.style.display='none';
    document.getElementById('password').parentNode.style.display='none';
    document.getElementById('successMsg').style.display='';
  } catch(e) { document.getElementById('formError').textContent='网络错误，请重试'; document.getElementById('formError').style.display=''; document.getElementById('registerBtn').disabled=false; document.getElementById('registerBtn').textContent='注册'; }
}
</script>
</body>
</html>`

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
