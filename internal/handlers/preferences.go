package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
)

// PreferencesHandler manages per-user UI preferences.
type PreferencesHandler struct {
	DB *gorm.DB
}

// Get returns the current user's preferences.
// GET /api/v1/preferences
func (h *PreferencesHandler) Get(c *gin.Context) {
	email, _ := c.Get("user_email")

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var user models.User
	if err := h.DB.WithContext(ctx).Select("preferences").Where("email = ?", email).Take(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	// Return raw JSON or empty object
	var prefs map[string]interface{}
	if user.Preferences != nil {
		_ = json.Unmarshal(user.Preferences, &prefs)
	}
	if prefs == nil {
		prefs = map[string]interface{}{}
	}
	c.JSON(http.StatusOK, prefs)
}

// Update merges incoming keys into the current user's preferences.
// PUT /api/v1/preferences
func (h *PreferencesHandler) Update(c *gin.Context) {
	email, _ := c.Get("user_email")

	var incoming map[string]interface{}
	if err := c.ShouldBindJSON(&incoming); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var user models.User
	if err := h.DB.WithContext(ctx).Where("email = ?", email).Take(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	// Merge existing prefs with incoming
	existing := map[string]interface{}{}
	if user.Preferences != nil {
		_ = json.Unmarshal(user.Preferences, &existing)
	}
	for k, v := range incoming {
		existing[k] = v
	}

	merged, _ := json.Marshal(existing)
	if err := h.DB.WithContext(ctx).Model(&user).Update("preferences", datatypes.JSON(merged)).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save preferences"})
		return
	}

	c.JSON(http.StatusOK, existing)
}
