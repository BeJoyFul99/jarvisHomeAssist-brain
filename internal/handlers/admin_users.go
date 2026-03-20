package handlers

import (
	"context"
	"crypto/rand"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/smtp"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
)

// AdminUserHandler groups admin user-management dependencies.
type AdminUserHandler struct {
	DB *gorm.DB
}

// ── helpers ────────────────────────────────────────────────

func (h *AdminUserHandler) callerPermissions(c *gin.Context) models.Permissions {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	email, _ := c.Get("user_email")
	var caller models.User
	if err := h.DB.WithContext(ctx).Where("email = ?", email).First(&caller).Error; err != nil {
		return 0
	}
	return caller.Permissions
}

func (h *AdminUserHandler) requirePerm(c *gin.Context, required models.Permissions) bool {
	if !h.callerPermissions(c).Has(required) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "insufficient_permissions",
			"message": "You do not have permission to perform this action",
		})
		return false
	}
	return true
}

func dbCtx(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), 5*time.Second)
}

// validateResourcePerms checks that every permission string is valid.
func validateResourcePerms(perms []string) []string {
	var invalid []string
	for _, p := range perms {
		if !models.IsValidResourcePerm(p) {
			invalid = append(invalid, p)
		}
	}
	return invalid
}

// ── Permissions Schema ────────────────────────────────────

// PermissionsSchema handles GET /api/v1/admin/permissions/schema
// Returns the permission categories and defaults so the frontend can render toggles.
func (h *AdminUserHandler) PermissionsSchema(c *gin.Context) {
	type categoryItem struct {
		Name  string   `json:"name"`
		Perms []string `json:"perms"`
	}
	categories := make([]categoryItem, 0, len(models.PermCategoryOrder))
	for _, name := range models.PermCategoryOrder {
		categories = append(categories, categoryItem{
			Name:  name,
			Perms: models.PermCategories[name],
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"categories": categories,
		"defaults": gin.H{
			"administrator": models.DefaultPermsForRole(models.RoleAdmin),
			"family_member": models.DefaultPermsForRole(models.RoleFamilyMember),
			"guest":         models.DefaultPermsForRole(models.RoleGuest),
		},
	})
}

// ── List users ─────────────────────────────────────────────

type userListItem struct {
	ID            uint               `json:"id"`
	Email         string             `json:"email"`
	DisplayName   string             `json:"display_name"`
	Phone         string             `json:"phone"`
	Role          string             `json:"role"`
	Permissions   models.Permissions `json:"permissions"`
	ResourcePerms json.RawMessage    `json:"resource_perms"`
	PermExpiresAt *time.Time         `json:"perm_expires_at"`
	IsLocked      bool               `json:"is_locked"`
	HasPIN        bool               `json:"has_pin"`
	AccessCount   int64              `json:"access_count"`
	LastLoginAt   *time.Time         `json:"last_login_at"`
	CreatedAt     time.Time          `json:"created_at"`
}

// ListUsers handles GET /api/v1/admin/users
func (h *AdminUserHandler) ListUsers(c *gin.Context) {
	if !h.requirePerm(c, models.PermViewUsers) {
		return
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	var users []models.User
	if err := h.DB.WithContext(ctx).Order("id asc").Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to fetch users"})
		return
	}

	list := make([]userListItem, len(users))
	for i, u := range users {
		rp := json.RawMessage(u.ResourcePerms)
		if rp == nil {
			rp = json.RawMessage("[]")
		}
		list[i] = userListItem{
			ID:            u.ID,
			Email:         u.Email,
			DisplayName:   u.DisplayName,
			Phone:         u.Phone,
			Role:          string(u.Role),
			Permissions:   u.Permissions,
			ResourcePerms: rp,
			PermExpiresAt: u.PermExpiresAt,
			IsLocked:      u.IsLocked,
			HasPIN:        u.GuestPIN != "",
			AccessCount:   u.AccessCount,
			LastLoginAt:   u.LastLoginAt,
			CreatedAt:     u.CreatedAt,
		}
	}

	c.JSON(http.StatusOK, gin.H{"users": list})
}

// ── Create user ────────────────────────────────────────────

type createUserRequest struct {
	Email         string   `json:"email" binding:"required,email"`
	DisplayName   string   `json:"display_name" binding:"required,min=1"`
	Password      string   `json:"password"` // optional for guests (PIN is auto-generated)
	Phone         string   `json:"phone"`
	Role          string   `json:"role" binding:"required,oneof=administrator family_member guest"`
	ResourcePerms []string `json:"resource_perms"`
	PermExpiresAt *string  `json:"perm_expires_at"`
}

// CreateUser handles POST /api/v1/admin/users
func (h *AdminUserHandler) CreateUser(c *gin.Context) {
	if !h.requirePerm(c, models.PermManageUsers) {
		return
	}

	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": err.Error()})
		return
	}

	isGuest := req.Role == "guest"

	// Non-guest users: if no adequate password provided, auto-generate one
	generatedPassword := ""
	if !isGuest {
		if len(req.Password) < 4 {
			// generate 12-char random password
			const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
			b := make([]byte, 12)
			for i := range b {
				n, _ := crand.Int(crand.Reader, big.NewInt(int64(len(letters))))
				b[i] = letters[n.Int64()]
			}
			generatedPassword = string(b)
			req.Password = generatedPassword
		}
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	// Check duplicate
	var existing models.User
	if err := h.DB.WithContext(ctx).Where("email = ?", req.Email).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "duplicate_email", "message": "A user with this email already exists"})
		return
	}

	// Determine resource permissions
	role := models.Role(req.Role)
	var resourcePerms datatypes.JSON
	if len(req.ResourcePerms) > 0 {
		if invalid := validateResourcePerms(req.ResourcePerms); len(invalid) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_permissions", "message": "Unknown permissions: " + invalid[0]})
			return
		}
		b, _ := json.Marshal(req.ResourcePerms)
		resourcePerms = datatypes.JSON(b)
	} else {
		resourcePerms = models.DefaultPermsJSON(role)
	}

	// Parse optional expiry
	var permExpiry *time.Time
	if req.PermExpiresAt != nil && *req.PermExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.PermExpiresAt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": "Invalid perm_expires_at format (use RFC3339)"})
			return
		}
		permExpiry = &t
	}

	user := models.User{
		Email:         req.Email,
		DisplayName:   req.DisplayName,
		Phone:         req.Phone,
		Role:          role,
		ResourcePerms: resourcePerms,
		PermExpiresAt: permExpiry,
	}

	if user.Role == models.RoleAdmin {
		user.Permissions = models.AdminAll
	}

	// For guests: auto-generate a 6-digit PIN; set a random password (never used)
	var guestPIN string
	if isGuest {
		pin, err := models.GeneratePIN()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "pin_error", "message": "Failed to generate PIN"})
			return
		}
		guestPIN = pin
		if err := user.SetPIN(pin); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "hash_error", "message": "Failed to hash PIN"})
			return
		}
		// Set a random unusable password for guests
		randPw, _ := models.GeneratePIN()
		if err := user.SetPassword("guest-nologin-" + randPw); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "hash_error", "message": "Failed to hash password"})
			return
		}
	} else {
		if err := user.SetPassword(req.Password); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "hash_error", "message": "Failed to hash password"})
			return
		}
	}

	if err := h.DB.WithContext(ctx).Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to create user"})
		return
	}

	rp := json.RawMessage(user.ResourcePerms)
	resp := gin.H{
		"message": "User created",
		"user": userListItem{
			ID:            user.ID,
			Email:         user.Email,
			DisplayName:   user.DisplayName,
			Phone:         user.Phone,
			Role:          string(user.Role),
			Permissions:   user.Permissions,
			ResourcePerms: rp,
			PermExpiresAt: user.PermExpiresAt,
			IsLocked:      user.IsLocked,
			HasPIN:        user.GuestPIN != "",
			AccessCount:   0,
			CreatedAt:     user.CreatedAt,
		},
	}
	// Include plaintext PIN for guest — only time it's visible
	if isGuest {
		resp["guest_pin"] = guestPIN
	}
	// Include generated password for member if we auto-generated one
	if generatedPassword != "" {
		resp["generated_password"] = generatedPassword
	}
	c.JSON(http.StatusCreated, resp)
}

// ── Regenerate Guest PIN ──────────────────────────────────

// RegeneratePIN handles POST /api/v1/admin/users/:id/regenerate-pin
func (h *AdminUserHandler) RegeneratePIN(c *gin.Context) {
	if !h.requirePerm(c, models.PermManageUsers) {
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id", "message": "Invalid user ID"})
		return
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	var user models.User
	if err := h.DB.WithContext(ctx).First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "User not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to fetch user"})
		return
	}

	if user.Role != models.RoleGuest {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not_guest", "message": "PIN can only be regenerated for guest users"})
		return
	}

	pin, err := models.GeneratePIN()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "pin_error", "message": "Failed to generate PIN"})
		return
	}

	if err := user.SetPIN(pin); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash_error", "message": "Failed to hash PIN"})
		return
	}

	// Increment token_rev to invalidate existing sessions
	user.TokenRev++

	if err := h.DB.WithContext(ctx).Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to save new PIN"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":   "PIN regenerated successfully",
		"guest_pin": pin,
	})
}

// ── Update user ────────────────────────────────────────────

type updateUserRequest struct {
	Role          *string  `json:"role" binding:"omitempty,oneof=administrator family_member guest"`
	ResourcePerms []string `json:"resource_perms"`
	PermExpiresAt *string  `json:"perm_expires_at"`
}

// UpdateUser handles PATCH /api/v1/admin/users/:id
// Admins can change role, resource permissions, and permission expiry.
func (h *AdminUserHandler) UpdateUser(c *gin.Context) {
	if !h.requirePerm(c, models.PermManageUsers) {
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id", "message": "Invalid user ID"})
		return
	}

	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": err.Error()})
		return
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	var user models.User
	if err := h.DB.WithContext(ctx).First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "User not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to fetch user"})
		return
	}

	if req.Role != nil {
		user.Role = models.Role(*req.Role)
		if user.Role == models.RoleAdmin {
			user.Permissions = models.AdminAll
			user.ResourcePerms = models.DefaultPermsJSON(models.RoleAdmin)
		} else {
			user.Permissions = 0
			// If no explicit perms provided with role change, apply defaults
			if req.ResourcePerms == nil {
				user.ResourcePerms = models.DefaultPermsJSON(user.Role)
			}
		}
	}

	if req.ResourcePerms != nil {
		if invalid := validateResourcePerms(req.ResourcePerms); len(invalid) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_permissions", "message": "Unknown permissions: " + invalid[0]})
			return
		}
		b, _ := json.Marshal(req.ResourcePerms)
		user.ResourcePerms = datatypes.JSON(b)
	}

	if req.PermExpiresAt != nil {
		if *req.PermExpiresAt == "" || *req.PermExpiresAt == "null" {
			user.PermExpiresAt = nil
		} else {
			t, err := time.Parse(time.RFC3339, *req.PermExpiresAt)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": "Invalid perm_expires_at format (use RFC3339)"})
				return
			}
			user.PermExpiresAt = &t
		}
	}

	if err := h.DB.WithContext(ctx).Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to update user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "User updated"})
}

// ── Password Reset Request ────────────────────────────────

// RequestPasswordReset handles POST /api/v1/admin/users/:id/reset-password
func (h *AdminUserHandler) RequestPasswordReset(c *gin.Context) {
	if !h.requirePerm(c, models.PermManageUsers) {
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id", "message": "Invalid user ID"})
		return
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	var user models.User
	if err := h.DB.WithContext(ctx).First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "User not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to fetch user"})
		return
	}

	// Generate a secure reset token and persist it with an expiry
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token_error", "message": "Failed to generate reset token"})
		return
	}
	token := hex.EncodeToString(tokenBytes)
	expiry := time.Now().Add(1 * time.Hour)
	user.PasswordResetToken = token
	user.PasswordResetExpiry = &expiry

	if err := h.DB.WithContext(ctx).Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to save reset token"})
		return
	}

	// Build a frontend reset URL. FRONTEND_URL should be set in env (e.g. https://app.example.com)
	frontend := os.Getenv("FRONTEND_URL")
	if frontend == "" {
		frontend = "http://localhost:3000"
	}
	resetURL := fmt.Sprintf("%s/reset-password?uid=%d&token=%s", frontend, user.ID, token)

	// Try to send via SMTP if configured, otherwise log the reset link for developers.
	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	smtpUser := os.Getenv("SMTP_USER")
	smtpPass := os.Getenv("SMTP_PASS")
	from := os.Getenv("EMAIL_FROM")

	subject := "Password reset"
	body := fmt.Sprintf("You requested a password reset. Click the link to reset your password:\n\n%s\n\nThis link expires in 1 hour.", resetURL)

	if smtpHost != "" && smtpPort != "" && smtpUser != "" && smtpPass != "" && from != "" {
		to := []string{user.Email}
		msg := []byte("To: " + user.Email + "\r\n" +
			"Subject: " + subject + "\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: text/plain; charset=\"utf-8\"\r\n" +
			"\r\n" + body)
		auth := smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)
		if err := smtp.SendMail(smtpHost+":"+smtpPort, auth, from, to, msg); err != nil {
			log.Printf("[warn] failed to send password reset email to %s: %v", user.Email, err)
		}
	} else {
		// Not configured — log the reset link so operators/devs can use it in development
		log.Printf("[info] password reset link for %s: %s", user.Email, resetURL)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Password reset email sent to " + user.Email,
	})
}

// ── Delete user ────────────────────────────────────────────

func (h *AdminUserHandler) DeleteUser(c *gin.Context) {
	if !h.requirePerm(c, models.PermManageUsers) {
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id", "message": "Invalid user ID"})
		return
	}

	callerEmail, _ := c.Get("user_email")
	ctx, cancel := dbCtx(c)
	defer cancel()

	var user models.User
	if err := h.DB.WithContext(ctx).First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "User not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to fetch user"})
		return
	}

	if user.Email == callerEmail {
		c.JSON(http.StatusBadRequest, gin.H{"error": "self_delete", "message": "You cannot delete your own account"})
		return
	}

	if err := h.DB.WithContext(ctx).Delete(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to delete user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "User deleted"})
}

// ── Lock / Unlock ──────────────────────────────────────────

type lockRequest struct {
	IsLocked bool `json:"is_locked"`
}

func (h *AdminUserHandler) LockUser(c *gin.Context) {
	if !h.requirePerm(c, models.PermLockUsers) {
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id", "message": "Invalid user ID"})
		return
	}

	var req lockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": err.Error()})
		return
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	result := h.DB.WithContext(ctx).Model(&models.User{}).Where("id = ?", id).Update("is_locked", req.IsLocked)
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "User not found"})
		return
	}

	status := "unlocked"
	if req.IsLocked {
		status = "locked"
	}
	c.JSON(http.StatusOK, gin.H{"message": "User " + status})
}

// ── Revoke Tokens ──────────────────────────────────────────

func (h *AdminUserHandler) RevokeTokens(c *gin.Context) {
	if !h.requirePerm(c, models.PermRevokeTokens) {
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id", "message": "Invalid user ID"})
		return
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	result := h.DB.WithContext(ctx).Model(&models.User{}).Where("id = ?", id).
		Update("token_rev", gorm.Expr("token_rev + 1"))
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "User not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "All tokens revoked for this user"})
}
