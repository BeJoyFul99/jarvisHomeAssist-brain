package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/sse"
)

// WifiHandler manages home WiFi network CRUD.
type WifiHandler struct {
	DB  *gorm.DB
	Hub *sse.Hub
}

// ── List ──────────────────────────────────────────────────
// GET /api/v1/wifi — returns all WiFi networks.
// Accessible by any authenticated user; non-admins get passwords masked.
func (h *WifiHandler) List(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var networks []models.WifiNetwork
	if err := h.DB.WithContext(ctx).Order("id asc").Find(&networks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load wifi networks"})
		return
	}

	role, _ := c.Get("user_role")
	isAdmin := role == "administrator"

	type wifiResponse struct {
		ID          uint   `json:"id"`
		SSID        string `json:"ssid"`
		Password    string `json:"password"`
		Security    string `json:"security"`
		Band        string `json:"band"`
		Description string `json:"description"`
		IsGuest     bool   `json:"is_guest"`
		Enabled     bool   `json:"enabled"`
	}

	result := make([]wifiResponse, len(networks))
	for i, n := range networks {
		pw := n.Password
		if !isAdmin {
			pw = "" // non-admins don't see passwords in list — they use the QR/copy endpoint
		}
		result[i] = wifiResponse{
			ID:          n.ID,
			SSID:        n.SSID,
			Password:    pw,
			Security:    n.Security,
			Band:        n.Band,
			Description: n.Description,
			IsGuest:     n.IsGuest,
			Enabled:     n.Enabled,
		}
	}

	c.JSON(http.StatusOK, result)
}

// ── GetCredentials ────────────────────────────────────────
// GET /api/v1/wifi/:id/credentials — returns full credentials for a single network.
// Any authenticated user can access enabled networks (for QR code generation).
func (h *WifiHandler) GetCredentials(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var net models.WifiNetwork
	if err := h.DB.WithContext(ctx).First(&net, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "network not found"})
		return
	}

	if !net.Enabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "network is disabled"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ssid":     net.SSID,
		"password": net.Password,
		"security": net.Security,
	})
}

// ── Update ────────────────────────────────────────────────
// PATCH /api/v1/admin/wifi/:id — update SSID, password, or other fields.
// Admin only (enforced by route group).
func (h *WifiHandler) Update(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var body struct {
		SSID        *string `json:"ssid"`
		Password    *string `json:"password"`
		Security    *string `json:"security"`
		Band        *string `json:"band"`
		Description *string `json:"description"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var net models.WifiNetwork
	if err := h.DB.WithContext(ctx).First(&net, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "network not found"})
		return
	}

	if body.SSID != nil {
		net.SSID = *body.SSID
	}
	if body.Password != nil {
		net.Password = *body.Password
	}
	if body.Security != nil {
		net.Security = *body.Security
	}
	if body.Band != nil {
		net.Band = *body.Band
	}
	if body.Description != nil {
		net.Description = *body.Description
	}

	if err := h.DB.WithContext(ctx).Save(&net).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update"})
		return
	}

	h.Hub.Broadcast(sse.Event{Type: "wifi:updated", Data: net})

	c.JSON(http.StatusOK, net)
}

// ── Toggle ────────────────────────────────────────────────
// POST /api/v1/admin/wifi/:id/toggle — enable or disable a network.
func (h *WifiHandler) Toggle(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var net models.WifiNetwork
	if err := h.DB.WithContext(ctx).First(&net, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "network not found"})
		return
	}

	net.Enabled = !net.Enabled
	if err := h.DB.WithContext(ctx).Save(&net).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to toggle"})
		return
	}

	h.Hub.Broadcast(sse.Event{Type: "wifi:updated", Data: net})

	c.JSON(http.StatusOK, net)
}

// ── Create ────────────────────────────────────────────────
// POST /api/v1/admin/wifi — add a new WiFi network.
func (h *WifiHandler) Create(c *gin.Context) {
	var body struct {
		SSID        string `json:"ssid" binding:"required"`
		Password    string `json:"password" binding:"required"`
		Security    string `json:"security"`
		Band        string `json:"band"`
		Description string `json:"description"`
		IsGuest     bool   `json:"is_guest"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	net := models.WifiNetwork{
		SSID:        body.SSID,
		Password:    body.Password,
		Security:    cond(body.Security != "", body.Security, "WPA2"),
		Band:        cond(body.Band != "", body.Band, "5 GHz"),
		Description: body.Description,
		IsGuest:     body.IsGuest,
		Enabled:     true,
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	if err := h.DB.WithContext(ctx).Create(&net).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create network"})
		return
	}

	h.Hub.Broadcast(sse.Event{Type: "wifi:created", Data: net})

	c.JSON(http.StatusCreated, net)
}

// ── Delete ────────────────────────────────────────────────
// DELETE /api/v1/admin/wifi/:id — permanently remove a network.
func (h *WifiHandler) Delete(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	if err := h.DB.WithContext(ctx).Delete(&models.WifiNetwork{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete"})
		return
	}

	h.Hub.Broadcast(sse.Event{Type: "wifi:deleted", Data: gin.H{"id": id}})

	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func cond(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

// SeedDefaultWifiNetworks creates default WiFi networks if none exist.
func SeedDefaultWifiNetworks(db *gorm.DB, log *logger.Logger) {
	var count int64
	db.Model(&models.WifiNetwork{}).Count(&count)
	if count > 0 {
		return
	}

	defaults := []models.WifiNetwork{
		{
			SSID:        "HomeHub-5G",
			Password:    "MySecurePass2024!",
			Security:    "WPA2",
			Band:        "5 GHz",
			Description: "Main network — full access",
			IsGuest:     false,
			Enabled:     true,
		},
		{
			SSID:        "HomeHub-Guest",
			Password:    "Welcome2Home",
			Security:    "WPA2",
			Band:        "2.4 GHz",
			Description: "Guest network — internet only",
			IsGuest:     true,
			Enabled:     true,
		},
	}

	for _, n := range defaults {
		if err := db.Create(&n).Error; err != nil {
			log.Error("seed", fmt.Sprintf("wifi %s: %v", n.SSID, err))
		} else {
			log.Info("seed", fmt.Sprintf("wifi %s created", n.SSID))
		}
	}
}
