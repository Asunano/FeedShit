package app

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"feedshit/internal/database"
	"feedshit/internal/middleware"
)

// ========== Admin Handlers ==========

func (a *App) AdminLogin(c *gin.Context) {
	// Block login if setup hasn't been completed yet
	if a.DB.GetConfig("setup_complete") != "true" {
		c.JSON(http.StatusForbidden, gin.H{"error": "请先完成初始设置"})
		return
	}

	clientIP := middleware.GetClientIP(c)

	// Brute force protection
	if a.LoginTracker.IsLocked(clientIP) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": "登录尝试次数过多，请 15 分钟后再试",
		})
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

	// Try admins table first
	var role string
	var authenticated bool
	admin, err := a.DB.GetAdminByUsername(req.Username)
	if err == nil && admin != nil && admin.IsActive {
		if checkPassword(req.Password, admin.PasswordHash) {
			role = admin.Role
			authenticated = true
		}
	}

	// Fallback to legacy config-based admin (only valid if password is bcrypt-hashed)
	if !authenticated {
		dbUser := a.DB.GetConfig("admin_username")
		dbPwd := a.DB.GetConfig("admin_password")
		effectiveUser := a.Cfg.AdminUsername
		effectivePwd := a.Cfg.AdminPassword
		if dbUser != "" {
			effectiveUser = dbUser
		}
		if dbPwd != "" {
			effectivePwd = dbPwd
		}
		// Only accept bcrypt-hashed passwords from config — reject plaintext
		if isBcryptHash(effectivePwd) && middleware.SecureCompare(req.Username, effectiveUser) && checkPassword(req.Password, effectivePwd) {
			role = "admin"
			authenticated = true
		}
	}

	if !authenticated {
		a.LoginTracker.RecordFailure(clientIP)
		remaining := 10 - a.LoginTracker.FailureCount(clientIP)
		if remaining < 0 {
			remaining = 0
		}
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":     "用户名或密码错误",
			"remaining": remaining,
		})
		return
	}

	// Clear brute force tracker on success
	a.LoginTracker.ClearFailures(clientIP)

	token := a.SM.Create(req.Username, role)
	csrfToken := a.SM.GetCSRFToken(token)
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("admin_session", token, 86400, "/", "", a.cookieSecure(c), true)
	middleware.SetCSRFCookie(c, csrfToken, a.cookieSecure(c))

	a.DB.InsertAuditLog("login", "管理员登录", req.Username, clientIP)

	// Record last-login time for the matched admin account (table-based auth).
	if admin != nil {
		a.DB.UpdateAdminLastLogin(admin.ID, time.Now().Unix())
	}

	c.JSON(http.StatusOK, gin.H{"message": "登录成功", "role": role})
}

func (a *App) AdminLogout(c *gin.Context) {
	token, _ := c.Get("session_token")
	if t, ok := token.(string); ok {
		a.SM.Revoke(t)
	}
	c.SetCookie("admin_session", "", -1, "/", "", a.cookieSecure(c), true)
	c.SetCookie("csrf_token", "", -1, "/", "", false, false)
	c.JSON(http.StatusOK, gin.H{"message": "已退出"})
}

// AdminGetCSRFToken returns the CSRF token for the current session.
func (a *App) AdminGetCSRFToken(c *gin.Context) {
	token, _ := c.Get("session_token")
	t, ok := token.(string)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	csrfToken := a.SM.GetCSRFToken(t)
	c.JSON(http.StatusOK, gin.H{"csrf_token": csrfToken})
}

// ========== Current User ==========

// AdminGetCurrentUser returns the currently logged-in user's info.
func (a *App) AdminGetCurrentUser(c *gin.Context) {
	user, _ := c.Get("admin_user")
	role, _ := c.Get("admin_role")
	c.JSON(http.StatusOK, gin.H{
		"username": user,
		"role":     role,
	})
}

// ========== Admin Team Management ==========

// AdminListAdmins returns all admin accounts.
func (a *App) AdminListAdmins(c *gin.Context) {
	admins, err := a.DB.ListAdmins()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if admins == nil {
		admins = []database.Admin{}
	}
	c.JSON(http.StatusOK, gin.H{"admins": admins})
}

// AdminCreateAdmin creates a new admin account.
func (a *App) AdminCreateAdmin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		Grants   []struct {
			ProjectSlug string `json:"project_slug"`
			CategoryKey string `json:"category_key"`
			Role        string `json:"role"`
		} `json:"grants"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名不能为空"})
		return
	}
	if len(req.Username) < 3 || len(req.Username) > 32 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名长度 3-32 位"})
		return
	}
	if req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码不能为空"})
		return
	}
	if err := validatePasswordStrength(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	validRoles := map[string]bool{"admin": true, "manager": true, "editor": true, "viewer": true}
	if !validRoles[req.Role] {
		req.Role = "editor"
	}

	// Validate initial grants before creating the account (fail fast)
	var grants []database.MemberGrant
	if len(req.Grants) > 0 {
		grantRoles := map[string]bool{"viewer": true, "editor": true, "manager": true}
		grants = make([]database.MemberGrant, 0, len(req.Grants))
		for i, g := range req.Grants {
			if g.ProjectSlug == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("第 %d 条授权缺少 project_slug", i+1)})
				return
			}
			if g.CategoryKey == "" {
				g.CategoryKey = "*"
			}
			if !grantRoles[g.Role] {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("第 %d 条授权角色无效: %s", i+1, g.Role)})
				return
			}
			proj, perr := a.DB.GetProjectBySlug(g.ProjectSlug)
			if perr != nil || proj == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("项目不存在: %s", g.ProjectSlug)})
				return
			}
			if g.CategoryKey != "*" {
				cat, cerr := a.DB.GetCategoryByKey(g.ProjectSlug, g.CategoryKey)
				if cerr != nil || cat == nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("第 %d 条授权分类不存在: %s", i+1, g.CategoryKey)})
					return
				}
			}
			grants = append(grants, database.MemberGrant{ProjectSlug: g.ProjectSlug, CategoryKey: g.CategoryKey, Role: g.Role})
		}
	}

	// Check if username already exists
	existing, _ := a.DB.GetAdminByUsername(req.Username)
	if existing != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "用户名已存在"})
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}

	id, err := a.DB.CreateAdmin(req.Username, hash, req.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败"})
		return
	}

	// Persist initial grants so the new admin isn't empty-handed
	if len(grants) > 0 {
		if gerr := a.DB.SetMemberGrants(id, grants); gerr != nil {
			log.Printf("[ADMIN] failed to set initial grants for %s: %v", req.Username, gerr)
		}
	}

	currentUser, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("create_admin", fmt.Sprintf("创建管理员 %s (角色: %s, 授权 %d 条)", req.Username, req.Role, len(grants)), fmt.Sprintf("%v", currentUser), clientIP)

	c.JSON(http.StatusCreated, gin.H{"message": "管理员已创建", "id": id})
}

// AdminUpdateAdmin updates an existing admin's role, active status, or password.
func (a *App) AdminUpdateAdmin(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	var req struct {
		Role     string `json:"role"`
		IsActive *bool  `json:"is_active"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	admin, err := a.DB.GetAdminByID(id)
	if err != nil || admin == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "管理员不存在"})
		return
	}

	// Prevent self-deactivation
	currentUser, _ := c.Get("admin_user")
	if currentUser == admin.Username && req.IsActive != nil && !*req.IsActive {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能停用自己的账号"})
		return
	}

	validRoles := map[string]bool{"admin": true, "manager": true, "editor": true, "viewer": true}
	if !validRoles[req.Role] {
		req.Role = admin.Role
	}

	isActive := admin.IsActive
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	var pwdHash string
	if req.Password != "" {
		if err := validatePasswordStrength(req.Password); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		pwdHash, err = hashPassword(req.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
			return
		}
	}

	if err := a.DB.UpdateAdmin(id, req.Role, isActive, pwdHash); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败"})
		return
	}

	// F2: If password was changed, revoke all sessions for this admin
	if req.Password != "" {
		a.SM.RevokeUserSessions(admin.Username)
	}

	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("update_admin", fmt.Sprintf("更新管理员 %s", admin.Username), fmt.Sprintf("%v", currentUser), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "管理员已更新"})
}

// AdminDeleteAdmin deletes an admin account.
func (a *App) AdminDeleteAdmin(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	admin, err := a.DB.GetAdminByID(id)
	if err != nil || admin == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "管理员不存在"})
		return
	}

	// Prevent self-deletion
	currentUser, _ := c.Get("admin_user")
	if currentUser == admin.Username {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除自己的账号"})
		return
	}

	if err := a.DB.DeleteAdmin(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}

	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_admin", fmt.Sprintf("删除管理员 %s", admin.Username), fmt.Sprintf("%v", currentUser), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "管理员已删除"})
}

// ========== F1: Admin Reset Password ==========

// AdminResetPassword allows an admin user to reset another admin's password.
// Route: PUT /api/v1/admin/admins/:id/reset-password (admin only)
func (a *App) AdminResetPassword(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	admin, err := a.DB.GetAdminByID(id)
	if err != nil || admin == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "管理员不存在"})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码不能为空"})
		return
	}
	if err := validatePasswordStrength(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}

	if err := a.DB.UpdateAdminPassword(id, hash); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "重置失败"})
		return
	}

	// F2: Revoke all sessions for the reset admin
	a.SM.RevokeUserSessions(admin.Username)

	currentUser, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("reset_password", fmt.Sprintf("重置管理员 %s 的密码", admin.Username), fmt.Sprintf("%v", currentUser), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "密码已重置"})
}

// ========== Member Grants (Fine-grained RBAC) ==========

// AdminGetMemberGrants returns all grants for a specific admin.
func (a *App) AdminGetMemberGrants(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	admin, err := a.DB.GetAdminByID(id)
	if err != nil || admin == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "管理员不存在"})
		return
	}

	grants, err := a.DB.ListMemberGrants(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if grants == nil {
		grants = []database.MemberGrant{}
	}
	c.JSON(http.StatusOK, gin.H{"grants": grants})
}

// AdminSetMemberGrants replaces all grants for an admin with the provided list.
func (a *App) AdminSetMemberGrants(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	admin, err := a.DB.GetAdminByID(id)
	if err != nil || admin == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "管理员不存在"})
		return
	}

	var req struct {
		Grants []struct {
			ProjectSlug string `json:"project_slug"`
			CategoryKey string `json:"category_key"`
			Role        string `json:"role"`
		} `json:"grants"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	validRoles := map[string]bool{"viewer": true, "editor": true, "manager": true}
	grants := make([]database.MemberGrant, 0, len(req.Grants))
	for i, g := range req.Grants {
		if g.ProjectSlug == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("第 %d 条授权缺少 project_slug", i+1)})
			return
		}
		if g.CategoryKey == "" {
			g.CategoryKey = "*"
		}
		if !validRoles[g.Role] {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("第 %d 条授权角色无效: %s", i+1, g.Role)})
			return
		}
		// Verify project exists
		proj, err := a.DB.GetProjectBySlug(g.ProjectSlug)
		if err != nil || proj == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("项目不存在: %s", g.ProjectSlug)})
			return
		}
		// Verify category_key exists in project dictionary (wildcard "*" is always allowed)
		if g.CategoryKey != "*" {
			cat, catErr := a.DB.GetCategoryByKey(g.ProjectSlug, g.CategoryKey)
			if catErr != nil || cat == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("第 %d 条授权分类不存在: %s", i+1, g.CategoryKey)})
				return
			}
		}
		grants = append(grants, database.MemberGrant{
			ProjectSlug: g.ProjectSlug,
			CategoryKey: g.CategoryKey,
			Role:        g.Role,
		})
	}

	if err := a.DB.SetMemberGrants(id, grants); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "设置失败"})
		return
	}

	currentUser, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("set_member_grants", fmt.Sprintf("设置 %s 的授权 (%d 条)", admin.Username, len(grants)), fmt.Sprintf("%v", currentUser), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "授权已更新", "count": len(grants)})
}

// AdminDeleteMemberGrant removes a single grant by ID.
func (a *App) AdminDeleteMemberGrant(c *gin.Context) {
	grantID, err := strconv.ParseInt(c.Param("grantId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的授权 ID"})
		return
	}

	if err := a.DB.DeleteMemberGrant(grantID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}

	currentUser, _ := c.Get("admin_user")
	clientIP := middleware.GetClientIP(c)
	a.DB.InsertAuditLog("delete_member_grant", fmt.Sprintf("删除授权 #%d", grantID), fmt.Sprintf("%v", currentUser), clientIP)

	c.JSON(http.StatusOK, gin.H{"message": "授权已撤销"})
}
