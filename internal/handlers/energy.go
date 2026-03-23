package handlers

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/sse"
)

// EnergyHandler manages energy usage data.
type EnergyHandler struct {
	DB  *gorm.DB
	Hub *sse.Hub
}

// ── Today ──────────────────────────────────────────────────
// GET /api/v1/energy/today — returns hourly readings for today.
func (h *EnergyHandler) Today(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	var readings []models.EnergyReading
	if err := h.DB.WithContext(ctx).
		Where("timestamp >= ? AND timestamp < ?", startOfDay, startOfDay.Add(24*time.Hour)).
		Order("timestamp asc").
		Find(&readings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load readings"})
		return
	}

	c.JSON(http.StatusOK, readings)
}

// ── Range ──────────────────────────────────────────────────
// GET /api/v1/energy?from=2026-03-01&to=2026-03-22 — returns readings for a date range.
func (h *EnergyHandler) Range(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	fromStr := c.Query("from")
	toStr := c.Query("to")

	from, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		from = time.Now().AddDate(0, 0, -7) // default last 7 days
	}
	to, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		to = time.Now()
	}
	to = to.Add(24 * time.Hour) // include end date

	var readings []models.EnergyReading
	if err := h.DB.WithContext(ctx).
		Where("timestamp >= ? AND timestamp < ?", from, to).
		Order("timestamp asc").
		Find(&readings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load readings"})
		return
	}

	c.JSON(http.StatusOK, readings)
}

// ── Summary ──────────────────────────────────────────────────
// GET /api/v1/energy/summary?period=day|week|month — aggregated stats.
func (h *EnergyHandler) Summary(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	period := c.DefaultQuery("period", "day")
	now := time.Now()

	var from time.Time
	switch period {
	case "week":
		from = now.AddDate(0, 0, -7)
	case "month":
		from = now.AddDate(0, -1, 0)
	default: // day
		from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	}

	var readings []models.EnergyReading
	if err := h.DB.WithContext(ctx).
		Where("timestamp >= ?", from).
		Order("timestamp asc").
		Find(&readings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load readings"})
		return
	}

	// Load active rates for cost calculation
	var rates []models.EnergyRate
	h.DB.WithContext(ctx).Where("is_active = ?", true).Find(&rates)

	totalWh := 0.0
	totalCost := 0.0
	peakWatts := 0.0
	var avgWattsSum float64

	for _, r := range readings {
		totalWh += r.WattHours
		avgWattsSum += r.AvgWatts
		if r.PeakWatts > peakWatts {
			peakWatts = r.PeakWatts
		}
		// Calculate cost for this hour based on rate
		cost := r.WattHours / 1000.0 * getRate(rates, r.Timestamp.Hour())
		totalCost += cost
	}

	avgWatts := 0.0
	if len(readings) > 0 {
		avgWatts = avgWattsSum / float64(len(readings))
	}

	totalKWh := totalWh / 1000.0

	// Get budget for current month
	var budget models.EnergyBudget
	h.DB.WithContext(ctx).
		Where("month = ? AND year = ?", int(now.Month()), now.Year()).
		First(&budget)

	// Daily breakdown for charts
	type dailyEntry struct {
		Date string  `json:"date"`
		KWh  float64 `json:"kwh"`
		Cost float64 `json:"cost"`
	}

	dailyMap := map[string]*dailyEntry{}
	for _, r := range readings {
		key := r.Timestamp.Format("2006-01-02")
		if _, ok := dailyMap[key]; !ok {
			dailyMap[key] = &dailyEntry{Date: key}
		}
		dailyMap[key].KWh += r.WattHours / 1000.0
		dailyMap[key].Cost += r.WattHours / 1000.0 * getRate(rates, r.Timestamp.Hour())
	}

	daily := make([]dailyEntry, 0, len(dailyMap))
	for _, v := range dailyMap {
		v.KWh = math.Round(v.KWh*100) / 100
		v.Cost = math.Round(v.Cost*100) / 100
		daily = append(daily, *v)
	}

	currency := GetSetting(h.DB, "currency", "CAD")

	c.JSON(http.StatusOK, gin.H{
		"period":        period,
		"total_kwh":     math.Round(totalKWh*100) / 100,
		"total_cost":    math.Round(totalCost*100) / 100,
		"avg_watts":     math.Round(avgWatts*10) / 10,
		"peak_watts":    peakWatts,
		"reading_count": len(readings),
		"currency":      currency,
		"budget_kwh":    budget.BudgetKWh,
		"budget_amount": budget.BudgetAmount,
		"daily":         daily,
	})
}

// ── Record ──────────────────────────────────────────────────
// POST /api/v1/admin/energy — record a new energy reading.
func (h *EnergyHandler) Record(c *gin.Context) {
	var body struct {
		Timestamp string  `json:"timestamp"` // ISO 8601 or "2006-01-02T15:04"
		WattHours float64 `json:"watt_hours" binding:"required"`
		AvgWatts  float64 `json:"avg_watts"`
		PeakWatts float64 `json:"peak_watts"`
		Source    string  `json:"source"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ts := time.Now().Truncate(time.Hour)
	if body.Timestamp != "" {
		parsed, err := time.Parse(time.RFC3339, body.Timestamp)
		if err != nil {
			parsed, err = time.Parse("2006-01-02T15:04", body.Timestamp)
		}
		if err == nil {
			ts = parsed.Truncate(time.Hour)
		}
	}

	if body.AvgWatts == 0 && body.WattHours > 0 {
		body.AvgWatts = body.WattHours // Wh in 1 hour ≈ average watts
	}

	reading := models.EnergyReading{
		Timestamp: ts,
		WattHours: body.WattHours,
		AvgWatts:  body.AvgWatts,
		PeakWatts: body.PeakWatts,
		Source:    cond(body.Source != "", body.Source, "manual"),
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	if err := h.DB.WithContext(ctx).Create(&reading).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record reading"})
		return
	}

	h.Hub.Broadcast(sse.Event{Type: "energy:reading", Data: reading})
	c.JSON(http.StatusCreated, reading)
}

// ── Batch Record ──────────────────────────────────────────────
// POST /api/v1/admin/energy/batch — record multiple readings at once.
func (h *EnergyHandler) BatchRecord(c *gin.Context) {
	var body struct {
		Readings []struct {
			Timestamp string  `json:"timestamp"`
			WattHours float64 `json:"watt_hours"`
			AvgWatts  float64 `json:"avg_watts"`
			PeakWatts float64 `json:"peak_watts"`
			Source    string  `json:"source"`
		} `json:"readings" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	created := 0
	for _, r := range body.Readings {
		ts := time.Now().Truncate(time.Hour)
		if r.Timestamp != "" {
			parsed, err := time.Parse(time.RFC3339, r.Timestamp)
			if err != nil {
				parsed, err = time.Parse("2006-01-02T15:04", r.Timestamp)
			}
			if err == nil {
				ts = parsed.Truncate(time.Hour)
			}
		}

		avgW := r.AvgWatts
		if avgW == 0 && r.WattHours > 0 {
			avgW = r.WattHours
		}

		reading := models.EnergyReading{
			Timestamp: ts,
			WattHours: r.WattHours,
			AvgWatts:  avgW,
			PeakWatts: r.PeakWatts,
			Source:    cond(r.Source != "", r.Source, "manual"),
		}

		if err := h.DB.WithContext(ctx).Create(&reading).Error; err == nil {
			created++
		}
	}

	c.JSON(http.StatusCreated, gin.H{"created": created, "total": len(body.Readings)})
}

// ── Rates ──────────────────────────────────────────────────
// GET /api/v1/energy/rates — list all energy rates.
func (h *EnergyHandler) ListRates(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var rates []models.EnergyRate
	if err := h.DB.WithContext(ctx).Order("start_hour asc").Find(&rates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load rates"})
		return
	}
	c.JSON(http.StatusOK, rates)
}

// POST /api/v1/admin/energy/rates — create or update a rate.
func (h *EnergyHandler) UpsertRate(c *gin.Context) {
	var body struct {
		ID          *uint   `json:"id"`
		Name        string  `json:"name" binding:"required"`
		PricePerKWh float64 `json:"price_per_kwh" binding:"required"`
		Currency    string  `json:"currency"`
		StartHour   int     `json:"start_hour"`
		EndHour     int     `json:"end_hour"`
		IsActive    *bool   `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	rate := models.EnergyRate{
		Name:        body.Name,
		PricePerKWh: body.PricePerKWh,
		Currency:    cond(body.Currency != "", body.Currency, GetSetting(h.DB, "currency", "CAD")),
		StartHour:   body.StartHour,
		EndHour:     cond_int(body.EndHour > 0, body.EndHour, 24),
		IsActive:    body.IsActive == nil || *body.IsActive,
	}

	if body.ID != nil && *body.ID > 0 {
		rate.ID = *body.ID
		if err := h.DB.WithContext(ctx).Save(&rate).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update rate"})
			return
		}
	} else {
		if err := h.DB.WithContext(ctx).Create(&rate).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create rate"})
			return
		}
	}

	h.Hub.Broadcast(sse.Event{Type: "energy:rate_updated", Data: rate})
	c.JSON(http.StatusOK, rate)
}

// DELETE /api/v1/admin/energy/rates/:id
func (h *EnergyHandler) DeleteRate(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	if err := h.DB.WithContext(ctx).Delete(&models.EnergyRate{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete rate"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// ── Budget ──────────────────────────────────────────────────
// GET /api/v1/energy/budget?month=3&year=2026
func (h *EnergyHandler) GetBudget(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	now := time.Now()
	month, _ := strconv.Atoi(c.DefaultQuery("month", strconv.Itoa(int(now.Month()))))
	year, _ := strconv.Atoi(c.DefaultQuery("year", strconv.Itoa(now.Year())))

	var budget models.EnergyBudget
	result := h.DB.WithContext(ctx).Where("month = ? AND year = ?", month, year).First(&budget)
	if result.Error != nil {
		c.JSON(http.StatusOK, gin.H{"month": month, "year": year, "budget_kwh": 0, "budget_amount": 0, "currency": GetSetting(h.DB, "currency", "CAD")})
		return
	}
	c.JSON(http.StatusOK, budget)
}

// POST /api/v1/admin/energy/budget — set monthly budget.
func (h *EnergyHandler) SetBudget(c *gin.Context) {
	var body struct {
		Month        int     `json:"month" binding:"required"`
		Year         int     `json:"year" binding:"required"`
		BudgetKWh    float64 `json:"budget_kwh"`
		BudgetAmount float64 `json:"budget_amount"`
		Currency     string  `json:"currency"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var budget models.EnergyBudget
	result := h.DB.WithContext(ctx).Where("month = ? AND year = ?", body.Month, body.Year).First(&budget)
	if result.Error != nil {
		// Create new
		budget = models.EnergyBudget{
			Month:        body.Month,
			Year:         body.Year,
			BudgetKWh:    body.BudgetKWh,
			BudgetAmount: body.BudgetAmount,
			Currency:     cond(body.Currency != "", body.Currency, GetSetting(h.DB, "currency", "CAD")),
		}
		h.DB.WithContext(ctx).Create(&budget)
	} else {
		budget.BudgetKWh = body.BudgetKWh
		budget.BudgetAmount = body.BudgetAmount
		if body.Currency != "" {
			budget.Currency = body.Currency
		}
		h.DB.WithContext(ctx).Save(&budget)
	}

	h.Hub.Broadcast(sse.Event{Type: "energy:budget_updated", Data: budget})
	c.JSON(http.StatusOK, budget)
}

// ── Helpers ──────────────────────────────────────────────────

func getRate(rates []models.EnergyRate, hour int) float64 {
	for _, r := range rates {
		if hour >= r.StartHour && hour < r.EndHour {
			return r.PricePerKWh
		}
	}
	// Default rate if none configured
	return 0.12
}

func cond_int(ok bool, a, b int) int {
	if ok {
		return a
	}
	return b
}

