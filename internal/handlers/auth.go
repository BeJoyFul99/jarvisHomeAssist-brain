package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/config"
	"jarvishomeassist-brain/internal/models"
)

// AuthHandler groups login-related dependencies.
type AuthHandler struct {
	DB  *gorm.DB
	Cfg *config.Config
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=4"`
}

type pinLoginRequest struct {
	PIN   string `json:"pin" binding:"required,len=6"`
	Email string `json:"email" binding:"omitempty"`
	Name  string `json:"name" binding:"omitempty"`
}

type loginResponse struct {
	Token string       `json:"token"`
	User  userResponse `json:"user"`
}

type userResponse struct {
	ID          uint       `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

// issueJWT builds and signs a JWT for the given user.
func (h *AuthHandler) issueJWT(user *models.User) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":            user.Email,
		"name":           user.DisplayName,
		"email":          user.Email,
		"role":           string(user.Role),
		"resource_perms": user.GetResourcePerms(),
		"perm_expires_at": func() interface{} {
			if user.PermExpiresAt != nil {
				return user.PermExpiresAt.Format(time.RFC3339)
			}
			return nil
		}(),
		"token_rev": user.TokenRev,
		"iat":       time.Now().Unix(),
		"exp":       time.Now().Add(h.Cfg.JWTExpiry).Unix(),
	})
	return token.SignedString([]byte(h.Cfg.JWTSecret))
}

// recordLogin updates access tracking fields.
func (h *AuthHandler) recordLogin(ctx context.Context, user *models.User) {
	now := time.Now().UTC()
	h.DB.WithContext(ctx).Model(user).Updates(map[string]interface{}{
		"last_login_at": now,
		"access_count":  gorm.Expr("access_count + 1"),
	})
	user.LastLoginAt = &now
}

// Login handles POST /auth/login (email + password).
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "validation_error",
			"message": err.Error(),
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var user models.User
	result := h.DB.WithContext(ctx).Where("email = ?", req.Email).First(&user)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":   "invalid_credentials",
				"message": "Invalid email or password",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "database_error",
			"message": "An internal error occurred",
		})
		return
	}

	if user.IsLocked {
		c.JSON(http.StatusForbidden, gin.H{
			"error":   "account_locked",
			"message": "Your account has been locked. Contact an administrator.",
		})
		return
	}

	if !user.CheckPassword(req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":   "invalid_credentials",
			"message": "Invalid email or password",
		})
		return
	}

	h.recordLogin(ctx, &user)

	signed, err := h.issueJWT(&user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "token_error",
			"message": "Failed to generate token",
		})
		return
	}

	c.JSON(http.StatusOK, loginResponse{
		Token: signed,
		User: userResponse{
			ID:          user.ID,
			Email:       user.Email,
			DisplayName: user.DisplayName,
			Role:        string(user.Role),
			LastLoginAt: user.LastLoginAt,
		},
	})
}

// PINLogin handles POST /auth/pin-login (email + 6-digit PIN for guests).
func (h *AuthHandler) PINLogin(c *gin.Context) {
	var req pinLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "validation_error",
			"message": err.Error(),
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var user models.User

	// If an email is provided, use the existing flow (email + PIN)
	if req.Email != "" {
		result := h.DB.WithContext(ctx).Where("email = ? AND role = ?", req.Email, models.RoleGuest).First(&user)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_credentials", "message": "Invalid email or PIN"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "An internal error occurred"})
			return
		}
	} else if req.Name != "" {
		// Name-based guest login
		result := h.DB.WithContext(ctx).Where("display_name = ? AND role = ?", req.Name, models.RoleGuest).First(&user)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_credentials", "message": "Invalid name or PIN"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "An internal error occurred"})
			return
		}
	} else {
		// No email provided: allow PIN-only guest login by searching guest users
		var candidates []models.User
		if err := h.DB.WithContext(ctx).Where("role = ? AND guest_pin <> ''", models.RoleGuest).Find(&candidates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to fetch users"})
			return
		}
		found := false
		for _, u := range candidates {
			if u.CheckPIN(req.PIN) {
				user = u
				found = true
				break
			}
		}
		if !found {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_credentials", "message": "Invalid PIN"})
			return
		}
	}

	if user.IsLocked {
		c.JSON(http.StatusForbidden, gin.H{
			"error":   "account_locked",
			"message": "Your guest account has been locked. Contact the homeowner.",
		})
		return
	}

	if user.IsGuestExpired() {
		c.JSON(http.StatusForbidden, gin.H{
			"error":   "access_expired",
			"message": "Your guest access has expired. Contact the homeowner.",
		})
		return
	}

	if !user.CheckPIN(req.PIN) {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":   "invalid_credentials",
			"message": "Invalid email or PIN",
		})
		return
	}

	h.recordLogin(ctx, &user)

	signed, err := h.issueJWT(&user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "token_error",
			"message": "Failed to generate token",
		})
		return
	}

	c.JSON(http.StatusOK, loginResponse{
		Token: signed,
		User: userResponse{
			ID:          user.ID,
			Email:       user.Email,
			DisplayName: user.DisplayName,
			Role:        string(user.Role),
			LastLoginAt: user.LastLoginAt,
		},
	})
}

type resetPasswordRequest struct {
	UID      uint   `json:"uid" binding:"required"`
	Token    string `json:"token" binding:"required"`
	Password string `json:"password" binding:"required,min=4"`
}

// ResetPassword handles POST /auth/reset-password
// Expects { uid, token, password } — verifies token + expiry then sets new password.
func (h *AuthHandler) ResetPassword(c *gin.Context) {
	var req resetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var user models.User
	if err := h.DB.WithContext(ctx).First(&user, req.UID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "User not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to fetch user"})
		return
	}

	// Validate token and expiry
	if req.Token == "" || user.PasswordResetToken == "" || user.PasswordResetExpiry == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_token", "message": "Invalid or expired token"})
		return
	}
	if user.PasswordResetToken != req.Token || time.Now().After(*user.PasswordResetExpiry) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_token", "message": "Invalid or expired token"})
		return
	}

	// Set new password
	if err := user.SetPassword(req.Password); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash_error", "message": "Failed to hash password"})
		return
	}

	// Clear reset token and expiry and bump token rev to invalidate sessions
	user.PasswordResetToken = ""
	user.PasswordResetExpiry = nil
	user.TokenRev++

	if err := h.DB.WithContext(ctx).Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to save new password"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Password reset successful"})
}
