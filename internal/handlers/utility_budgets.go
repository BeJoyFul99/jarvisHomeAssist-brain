package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/bills"
	"jarvishomeassist-brain/internal/models"
)

// UtilityBudgetHandler exposes per-property budget endpoints. Same table as
// EnergyBudget — PropertyID distinguishes utility budgets from legacy ones.
type UtilityBudgetHandler struct {
	DB *gorm.DB
}

// GET /api/v1/utility-budgets?property_id=<id>
func (h *UtilityBudgetHandler) List(c *gin.Context) {
	q := h.DB.WithContext(c.Request.Context()).Model(&models.EnergyBudget{}).Order("year desc, month desc")
	if pid := c.Query("property_id"); pid != "" {
		q = q.Where("property_id = ?", pid)
	}
	var out []models.EnergyBudget
	if err := q.Find(&out).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list"})
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /api/v1/utility-budgets — upsert (unique by property+month+year).
func (h *UtilityBudgetHandler) Upsert(c *gin.Context) {
	var body struct {
		Month             int     `json:"month" binding:"required"`
		Year              int     `json:"year" binding:"required"`
		PropertyID        *uint   `json:"property_id"`
		BudgetKWh         float64 `json:"budget_kwh"`
		BudgetAmount      float64 `json:"budget_amount"`
		Currency          string  `json:"currency"`
		AlertThresholdPct int     `json:"alert_threshold_pct"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	threshold := body.AlertThresholdPct
	if threshold <= 0 || threshold > 100 {
		threshold = 80
	}
	currency := body.Currency
	if currency == "" {
		currency = "CAD"
	}

	var b models.EnergyBudget
	q := h.DB.WithContext(c.Request.Context()).Where("month = ? AND year = ?", body.Month, body.Year)
	if body.PropertyID != nil {
		q = q.Where("property_id = ?", *body.PropertyID)
	} else {
		q = q.Where("property_id IS NULL")
	}
	err := q.First(&b).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup"})
		return
	}
	b.Month = body.Month
	b.Year = body.Year
	b.PropertyID = body.PropertyID
	b.BudgetKWh = body.BudgetKWh
	b.BudgetAmount = body.BudgetAmount
	b.Currency = currency
	b.AlertThresholdPct = threshold
	if err := h.DB.WithContext(c.Request.Context()).Save(&b).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save"})
		return
	}
	c.JSON(http.StatusOK, b)
}

// GET /api/v1/utility-budgets/pace?property_id=<id>&month=<m>&year=<y>
func (h *UtilityBudgetHandler) Pace(c *gin.Context) {
	pid, err := strconv.ParseUint(c.Query("property_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "property_id required"})
		return
	}
	month, _ := strconv.Atoi(c.DefaultQuery("month", strconv.Itoa(int(time.Now().Month()))))
	year, _ := strconv.Atoi(c.DefaultQuery("year", strconv.Itoa(time.Now().Year())))

	// Find() instead of First() — a month without a budget is expected and
	// should not be logged as an error by GORM.
	var budget models.EnergyBudget
	h.DB.WithContext(c.Request.Context()).
		Where("property_id = ? AND month = ? AND year = ?", pid, month, year).
		Limit(1).Find(&budget)

	proj, err := bills.ProjectCost(h.DB.WithContext(c.Request.Context()), uint(pid), month, year)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "project cost"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"projection": proj, "budget": budget})
}
