package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/config"
	"jarvishomeassist-brain/internal/models"
)

// sha256Hex returns the hex-encoded SHA-256 hash of s.
// Used instead of bcrypt because JWTs and refresh tokens exceed bcrypt's
// 72-byte input limit, and both are already high-entropy so a fast hash is safe.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

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
	Token        string       `json:"token"`
	RefreshToken string       `json:"refresh_token"`
	User         userResponse `json:"user"`
}

type userResponse struct {
	ID          uint       `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

// issueJWT builds, signs, and persists a short-lived JWT access token.
// The bcrypt hash of the signed token is stored on the user row so the
// middleware can verify it (and revocation is instant by clearing the column).
func (h *AuthHandler) issueJWT(ctx context.Context, user *models.User) (string, error) {
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
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(h.Cfg.JWTExpiry).Unix(),
	})

	signed, err := token.SignedString([]byte(h.Cfg.JWTSecret))
	if err != nil {
		return "", err
	}

	// Store a SHA-256 hash of the JWT so the middleware can validate it
	h.DB.WithContext(ctx).Model(user).Update("jwt_token", sha256Hex(signed))

	return signed, nil
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

// generateRefreshToken creates a random 64-byte hex token and stores its
// SHA-256 hash on the user row. Returns the raw token to send to the client.
func (h *AuthHandler) generateRefreshToken(ctx context.Context, user *models.User) (string, error) {
	raw := make([]byte, 64)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)

	h.DB.WithContext(ctx).Model(user).Update("refresh_token", sha256Hex(token))
	return token, nil
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
	result := h.DB.WithContext(ctx).Where("email = ?", req.Email).Take(&user)
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

	signed, err := h.issueJWT(ctx, &user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "token_error",
			"message": "Failed to generate token",
		})
		return
	}

	refreshToken, err := h.generateRefreshToken(ctx, &user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "token_error",
			"message": "Failed to generate refresh token",
		})
		return
	}

	c.JSON(http.StatusOK, loginResponse{
		Token:        signed,
		RefreshToken: refreshToken,
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
		result := h.DB.WithContext(ctx).Where("email = ? AND role = ?", req.Email, models.RoleGuest).Take(&user)
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
		result := h.DB.WithContext(ctx).Where("display_name = ? AND role = ?", req.Name, models.RoleGuest).Take(&user)
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

	signed, err := h.issueJWT(ctx, &user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "token_error",
			"message": "Failed to generate token",
		})
		return
	}

	refreshToken, err := h.generateRefreshToken(ctx, &user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "token_error",
			"message": "Failed to generate refresh token",
		})
		return
	}

	c.JSON(http.StatusOK, loginResponse{
		Token:        signed,
		RefreshToken: refreshToken,
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

	// Clear reset token/expiry and revoke all sessions (clear JWT + refresh tokens)
	user.PasswordResetToken = ""
	user.PasswordResetExpiry = nil
	user.JWTToken = ""
	user.RefreshToken = ""

	if err := h.DB.WithContext(ctx).Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to save new password"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Password reset successful"})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// RefreshToken handles POST /auth/refresh.
// Validates the refresh token, issues a new JWT + new refresh token (rotation).
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": "refresh_token is required"})
		return
	}

	// Extract the email from the expired JWT if present (allows refresh after expiry)
	var email string
	if header := c.GetHeader("Authorization"); header != "" && len(header) > 7 {
		tokenStr := header[7:]
		// Parse without validation so we can read claims from expired tokens
		parser := jwt.NewParser(jwt.WithoutClaimsValidation())
		token, _, err := parser.ParseUnverified(tokenStr, jwt.MapClaims{})
		if err == nil {
			if claims, ok := token.Claims.(jwt.MapClaims); ok {
				email, _ = claims["email"].(string)
			}
		}
	}

	if email == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_token", "message": "Could not identify user"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var user models.User
	if err := h.DB.WithContext(ctx).Where("email = ?", email).Take(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_token", "message": "User not found"})
		return
	}

	if user.IsLocked {
		c.JSON(http.StatusForbidden, gin.H{"error": "account_locked", "message": "Account is locked"})
		return
	}

	// Verify refresh token against stored SHA-256 hash
	if user.RefreshToken == "" || sha256Hex(req.RefreshToken) != user.RefreshToken {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_refresh_token", "message": "Refresh token is invalid or revoked"})
		return
	}

	// Issue new JWT
	signed, err := h.issueJWT(ctx, &user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token_error", "message": "Failed to generate token"})
		return
	}

	// Rotate refresh token (old one is now invalid)
	newRefresh, err := h.generateRefreshToken(ctx, &user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token_error", "message": "Failed to generate refresh token"})
		return
	}

	c.JSON(http.StatusOK, loginResponse{
		Token:        signed,
		RefreshToken: newRefresh,
		User: userResponse{
			ID:          user.ID,
			Email:       user.Email,
			DisplayName: user.DisplayName,
			Role:        string(user.Role),
			LastLoginAt: user.LastLoginAt,
		},
	})
}

// Logout handles POST /auth/logout.
// Clears both the JWT and refresh token from the database, instantly
// invalidating the user's session.
func (h *AuthHandler) Logout(c *gin.Context) {
	email, _ := c.Get("user_email")
	if email == nil || email == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_token", "message": "Could not identify user"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var user models.User
	if err := h.DB.WithContext(ctx).Where("email = ?", email).Take(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_token", "message": "User not found"})
		return
	}

	// Clear both tokens — JWT becomes invalid on next middleware check
	h.DB.WithContext(ctx).Model(&user).Updates(map[string]interface{}{
		"jwt_token":     "",
		"refresh_token": "",
	})

	c.JSON(http.StatusOK, gin.H{"message": "Logged out successfully"})
}
