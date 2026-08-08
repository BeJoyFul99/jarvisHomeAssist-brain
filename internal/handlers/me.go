package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
)

// MeHandler serves the current user's own profile.
type MeHandler struct {
	DB *gorm.DB
}

// Get returns the authenticated user's profile.
// GET /api/v1/me
func (h *MeHandler) Get(c *gin.Context) {
	uid, ok := c.Get("user_id")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized", "message": "Authentication required"})
		return
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	var user models.User
	if err := h.DB.WithContext(ctx).First(&user, uid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "User not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": newUserResponse(&user)})
}

type updateMeRequest struct {
	DisplayName string `json:"display_name" binding:"required,max=255"`
}

// Update changes the authenticated user's own profile fields.
// Only display_name is self-serviceable: email is the login identity baked
// into the JWT, so changing it here would invalidate the active session —
// email/role changes stay behind the admin user endpoints.
// PATCH /api/v1/me
func (h *MeHandler) Update(c *gin.Context) {
	uid, ok := c.Get("user_id")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized", "message": "Authentication required"})
		return
	}

	var req updateMeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": "display_name is required (max 255 characters)"})
		return
	}

	name := strings.TrimSpace(req.DisplayName)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": "display_name cannot be empty"})
		return
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	var user models.User
	if err := h.DB.WithContext(ctx).First(&user, uid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "User not found"})
		return
	}

	if err := h.DB.WithContext(ctx).Model(&user).Update("display_name", name).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database_error", "message": "Failed to update profile"})
		return
	}

	user.DisplayName = name
	c.JSON(http.StatusOK, gin.H{"user": newUserResponse(&user)})
}
