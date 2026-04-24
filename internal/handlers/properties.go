package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
)

// PropertyHandler manages utility service addresses / accounts.
type PropertyHandler struct {
	DB *gorm.DB
}

// GET /api/v1/properties — active properties only.
func (h *PropertyHandler) List(c *gin.Context) {
	var props []models.Property
	if err := h.DB.WithContext(c.Request.Context()).
		Where("is_active = ?", true).
		Order("id asc").
		Find(&props).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load properties"})
		return
	}
	c.JSON(http.StatusOK, props)
}

// POST /api/v1/properties
func (h *PropertyHandler) Create(c *gin.Context) {
	var body struct {
		Name          string `json:"name" binding:"required"`
		Address       string `json:"address" binding:"required"`
		AccountNumber string `json:"account_number"`
		Provider      string `json:"provider"`
		RateClass     string `json:"rate_class"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	prop := models.Property{
		Name:          body.Name,
		Address:       body.Address,
		AccountNumber: body.AccountNumber,
		Provider:      cond(body.Provider != "", body.Provider, "powerstream"),
		RateClass:     body.RateClass,
		IsActive:      true,
	}
	if err := h.DB.WithContext(c.Request.Context()).Create(&prop).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create property"})
		return
	}
	c.JSON(http.StatusCreated, prop)
}

// PATCH /api/v1/properties/:id
func (h *PropertyHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	allowed := map[string]bool{"name": true, "address": true, "account_number": true, "provider": true, "rate_class": true, "is_active": true}
	updates := map[string]any{}
	for k, v := range body {
		if allowed[k] {
			updates[k] = v
		}
	}
	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no updatable fields"})
		return
	}
	var prop models.Property
	if err := h.DB.WithContext(c.Request.Context()).First(&prop, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "property not found"})
		return
	}
	if err := h.DB.WithContext(c.Request.Context()).Model(&prop).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update"})
		return
	}
	// Reload to reflect updated fields in response.
	if err := h.DB.WithContext(c.Request.Context()).First(&prop, prop.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reload property"})
		return
	}
	c.JSON(http.StatusOK, prop)
}

// DELETE /api/v1/properties/:id — soft delete (is_active=false).
func (h *PropertyHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	res := h.DB.WithContext(c.Request.Context()).
		Model(&models.Property{}).
		Where("id = ?", id).
		Update("is_active", false)
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "property not found"})
		return
	}
	c.Status(http.StatusNoContent)
}
