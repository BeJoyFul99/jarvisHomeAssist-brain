package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/sse"
)

// SettingsHandler manages system-wide settings.
type SettingsHandler struct {
	DB  *gorm.DB
	Hub *sse.Hub
}

// List returns all settings as a key→value map.
// GET /api/v1/settings
func (h *SettingsHandler) List(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var rows []models.Setting
	if err := h.DB.WithContext(ctx).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load settings"})
		return
	}

	out := make(map[string]string, len(rows))
	for _, s := range rows {
		out[s.Key] = s.Value
	}
	c.JSON(http.StatusOK, out)
}

// Update creates or updates one or more settings.
// PUT /api/v1/admin/settings
func (h *SettingsHandler) Update(c *gin.Context) {
	var body map[string]string
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	for k, v := range body {
		setting := models.Setting{Key: k, Value: v}
		if err := h.DB.WithContext(ctx).Save(&setting).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save setting: " + k})
			return
		}
	}

	h.Hub.Broadcast(sse.Event{Type: "settings:updated", Data: body})
	c.JSON(http.StatusOK, body)
}

// SeedDefaultSettings ensures required settings exist with sensible defaults.
// Keep in sync with SERVER_SETTINGS in jarvishomeassist-face/lib/settingsSchema.ts
func SeedDefaultSettings(db *gorm.DB) {
	defaults := map[string]string{
		"currency":            "CAD",
		"terminal_logs":       "true",
		"polling_interval":    "2",
		"cpu_alert_threshold": "90",
		"auto_sleep_ai":       "true",
	}
	for k, v := range defaults {
		var existing models.Setting
		if db.Where("key = ?", k).First(&existing).Error != nil {
			db.Create(&models.Setting{Key: k, Value: v})
		}
	}
}

// GetSetting is a helper to read a single setting value, with a fallback.
func GetSetting(db *gorm.DB, key, fallback string) string {
	var s models.Setting
	if db.Where("key = ?", key).First(&s).Error != nil {
		return fallback
	}
	return s.Value
}
