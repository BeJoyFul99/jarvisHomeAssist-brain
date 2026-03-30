package handlers

import (
	"context"
	"crypto/rand"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/sse"
	"math/big"
	"net/http"
	"net/smtp"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// AdminUserHandler groups admin user-management dependencies.
type AdminUserHandler struct {
	DB  *gorm.DB
	Hub *sse.Hub
	Log *logger.Logger
}

// ── helpers ────────────────────────────────────────────────

// requireResourcePerm loads the caller from DB and checks a resource permission.
// Administrators always pass. Returns false (and aborts) if denied.
func (h *AdminUserHandler) requireResourcePerm(c *gin.Context, perm string) bool {
	// Administrators implicitly have every permission
	role, _ := c.Get("user_role")
	if role == "administrator" {
		return true
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	email, _ := c.Get("user_email")
	var caller models.User
	if err := h.DB.WithContext(ctx).Where("email = ?", email).First(&caller).Error; err != nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "insufficient_permissions",
			"message": "You do not have permission to perform this action",
		})
		return false
	}

	if !caller.HasResourcePerm(perm) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "insufficient_permissions",
			"message": "Missing permission: " + perm,
		})
		return false
	}
	return true
}

func dbCtx(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), 5*time.Second)
}

// callerInfo extracts the acting user's email and display name from the JWT context.
func callerInfo(c *gin.Context) (email string, name string) {
	if v, ok := c.Get("user_email"); ok {
		email, _ = v.(string)
	}
	if v, ok := c.Get("user_name"); ok {
		name, _ = v.(string)
	}
	return
}

// audit persists an audit log entry. Runs in a goroutine to avoid blocking the request.
func (h *AdminUserHandler) audit(c *gin.Context, action string, target models.User, details string) {
	actorEmail, actorName := callerInfo(c)
	// Look up actor ID from DB (best-effort)
	var actorID uint
	var actor models.User
	if err := h.DB.Where("email = ?", actorEmail).Select("id").First(&actor).Error; err == nil {
		actorID = actor.ID
	}
	entry := models.AuditLog{
		Action:      action,
		ActorID:     actorID,
		ActorEmail:  actorEmail,
		ActorName:   actorName,
		TargetID:    target.ID,
		TargetEmail: target.Email,
		TargetName:  target.DisplayName,
		Details:     details,
	}
	go func() {
		if err := h.DB.Create(&entry).Error; err != nil {
			h.Log.Error("audit", fmt.Sprintf("failed to write log: %v", err))
		}
	}()
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
	CreatedByID   uint               `json:"created_by_id"`
	CreatedByName string             `json:"created_by_name"`
	CreatedAt     time.Time          `json:"created_at"`
	DeletedAt     *time.Time         `json:"deleted_at"`
}

// ListUsers handles GET /api/v1/admin/users
func (h *AdminUserHandler) ListUsers(c *gin.Context) {
	if !h.requireResourcePerm(c, "user:view") {
		return
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	var users []models.User
	// Unscoped so soft-deleted users are included in the list
	if err := h.DB.WithContext(ctx).Unscoped().Order("deleted_at ASC NULLS FIRST, id ASC").Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to fetch users"})
		return
	}

	list := make([]userListItem, len(users))
	for i, u := range users {
		rp := json.RawMessage(u.ResourcePerms)
		if rp == nil {
			rp = json.RawMessage("[]")
		}
		var deletedAt *time.Time
		if u.DeletedAt.Valid {
			deletedAt = &u.DeletedAt.Time
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
			CreatedByID:   u.CreatedByID,
			CreatedByName: u.CreatedByName,
			CreatedAt:     u.CreatedAt,
			DeletedAt:     deletedAt,
		}
	}

	callerEmail, _ := c.Get("user_email")
	c.JSON(http.StatusOK, gin.H{"users": list, "caller_email": callerEmail})
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
	if !h.requireResourcePerm(c, "user:create_guest") {
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

	// Resolve caller for created_by tracking
	callerEmail, callerName := callerInfo(c)
	var callerID uint
	var callerUser models.User
	if err := h.DB.Where("email = ?", callerEmail).Select("id").First(&callerUser).Error; err == nil {
		callerID = callerUser.ID
	}

	user := models.User{
		Email:         req.Email,
		DisplayName:   req.DisplayName,
		Phone:         req.Phone,
		Role:          role,
		ResourcePerms: resourcePerms,
		PermExpiresAt: permExpiry,
		CreatedByID:   callerID,
		CreatedByName: callerName,
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
			CreatedByID:   user.CreatedByID,
			CreatedByName: user.CreatedByName,
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

	if h.Hub != nil {
		h.Hub.BroadcastUserEvent(sse.EventUserCreated, user.ID, user.Email, string(user.Role))
	}
	h.audit(c, models.AuditUserCreated, user, fmt.Sprintf("role=%s", user.Role))
 
}

// ── Regenerate Guest PIN ──────────────────────────────────

// RegeneratePIN handles POST /api/v1/admin/users/:id/regenerate-pin
func (h *AdminUserHandler) RegeneratePIN(c *gin.Context) {
	if !h.requireResourcePerm(c, "user:regenerate_pin") {
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

	// Clear JWT + refresh tokens to invalidate existing sessions
	user.JWTToken = ""
	user.RefreshToken = ""

	if err := h.DB.WithContext(ctx).Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to save new PIN"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":   "PIN regenerated successfully",
		"guest_pin": pin,
	})

	if h.Hub != nil {
		h.Hub.BroadcastUserEvent(sse.EventUserPinRegenerated, user.ID, user.Email, string(user.Role))
	}
	h.audit(c, models.AuditPINRegenerated, user, "")
}

// ── Update user ────────────────────────────────────────────

type updateUserRequest struct {
	Role          *string  `json:"role" binding:"omitempty,oneof=administrator family_member guest"`
	ResourcePerms []string `json:"resource_perms"`
	PermExpiresAt *string  `json:"perm_expires_at"`
}

// UpdateUser handles PATCH /api/v1/admin/users/:id
// Users with user:edit_perms can change role, resource permissions, and permission expiry.
func (h *AdminUserHandler) UpdateUser(c *gin.Context) {
	if !h.requireResourcePerm(c, "user:edit_perms") {
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

	if h.Hub != nil {
		h.Hub.BroadcastUserEvent(sse.EventUserUpdated, user.ID, user.Email, string(user.Role))
	}
	h.audit(c, models.AuditUserUpdated, user, fmt.Sprintf("role=%s", user.Role))
}

// ── Password Reset Request ────────────────────────────────

// RequestPasswordReset handles POST /api/v1/admin/users/:id/reset-password
func (h *AdminUserHandler) RequestPasswordReset(c *gin.Context) {
	if !h.requireResourcePerm(c, "user:edit_perms") {
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
			h.Log.Warn("admin", fmt.Sprintf("failed to send password reset email to %s: %v", user.Email, err))
		}
	} else {
		// Not configured — log the reset link so operators/devs can use it in development
		h.Log.Info("admin", fmt.Sprintf("password reset link for %s: %s", user.Email, resetURL))
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Password reset email sent to " + user.Email,
	})
	h.audit(c, models.AuditPasswordReset, user, "")
}

// ── Delete user ────────────────────────────────────────────

func (h *AdminUserHandler) DeleteUser(c *gin.Context) {
	if !h.requireResourcePerm(c, "user:delete_guest") {
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

	if h.Hub != nil {
		h.Hub.BroadcastUserEvent(sse.EventUserDeleted, user.ID, user.Email, string(user.Role))
	}
	h.audit(c, models.AuditUserDeleted, user, "")
}

// ── Restore soft-deleted user ─────────────────────────────

func (h *AdminUserHandler) RestoreUser(c *gin.Context) {
	if !h.requireResourcePerm(c, "user:delete_guest") {
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id", "message": "Invalid user ID"})
		return
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	// Find the soft-deleted user
	var user models.User
	if err := h.DB.WithContext(ctx).Unscoped().Where("id = ? AND deleted_at IS NOT NULL", id).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "Deleted user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to fetch user"})
		return
	}

	// Clear deleted_at to restore
	if err := h.DB.WithContext(ctx).Unscoped().Model(&user).Update("deleted_at", nil).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to restore user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "User restored", "user_id": user.ID})

	if h.Hub != nil {
		h.Hub.BroadcastUserEvent(sse.EventUserRestored, user.ID, user.Email, string(user.Role))
	}
	h.audit(c, models.AuditUserRestored, user, "")
}

// ── Hard-delete cleanup ──────────────────────────────────

// PurgeDeletedUsers permanently removes users that have been soft-deleted
// for more than 30 days. Intended to be called by a periodic cron/ticker.
func PurgeDeletedUsers(db *gorm.DB, log *logger.Logger) {
	cutoff := time.Now().AddDate(0, 0, -30)
	result := db.Unscoped().Where("deleted_at IS NOT NULL AND deleted_at < ?", cutoff).Delete(&models.User{})
	if result.Error != nil {
		log.Error("cron", fmt.Sprintf("purge deleted users error: %v", result.Error))
		return
	}
	if result.RowsAffected > 0 {
		log.Info("cron", fmt.Sprintf("purged %d users deleted >30 days ago", result.RowsAffected))
	}
}

// ── Lock / Unlock ──────────────────────────────────────────

type lockRequest struct {
	IsLocked bool `json:"is_locked"`
}

func (h *AdminUserHandler) LockUser(c *gin.Context) {
	if !h.requireResourcePerm(c, "user:lock") {
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

	var user models.User
	if err := h.DB.WithContext(ctx).First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "User not found"})
		return
	}

	if err := h.DB.WithContext(ctx).Model(&user).Update("is_locked", req.IsLocked).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to update user"})
		return
	}

	status := "unlocked"
	eventType := sse.EventUserUnlocked
	auditAction := models.AuditUserUnlocked
	if req.IsLocked {
		status = "locked"
		eventType = sse.EventUserLocked
		auditAction = models.AuditUserLocked
	}
	c.JSON(http.StatusOK, gin.H{"message": "User " + status})

	if h.Hub != nil {
		h.Hub.BroadcastUserEvent(eventType, user.ID, user.Email, string(user.Role))
	}
	h.audit(c, auditAction, user, "")
}

// ── Revoke Tokens ──────────────────────────────────────────

func (h *AdminUserHandler) RevokeTokens(c *gin.Context) {
	if !h.requireResourcePerm(c, "user:lock") {
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
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "User not found"})
		return
	}

	if err := h.DB.WithContext(ctx).Model(&user).Updates(map[string]interface{}{
		"jwt_token":     "",
		"refresh_token": "",
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to revoke tokens"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "All tokens revoked for this user"})

	if h.Hub != nil {
		h.Hub.BroadcastUserEvent(sse.EventUserTokensRevoked, user.ID, user.Email, string(user.Role))
	}
	h.audit(c, models.AuditTokensRevoked, user, "")
}

// ── Audit Logs ──────────────────────────────────────────────

// AuditLogs handles GET /api/v1/admin/audit-logs
// Optional query params: ?target_id=N to filter by target user, ?limit=50
func (h *AdminUserHandler) AuditLogs(c *gin.Context) {
	if !h.requireResourcePerm(c, "user:view") {
		return
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	limit := 100
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 && l <= 500 {
		limit = l
	}

	q := h.DB.WithContext(ctx).Order("created_at DESC").Limit(limit)

	if targetID := c.Query("target_id"); targetID != "" {
		q = q.Where("target_id = ?", targetID)
	}

	var logs []models.AuditLog
	if err := q.Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to fetch audit logs"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"logs": logs})
}
