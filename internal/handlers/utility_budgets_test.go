package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"jarvishomeassist-brain/internal/handlers"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func newBudgetRouter(t *testing.T) (*gin.Engine, *handlers.UtilityBudgetHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}, &models.EnergyBudget{}))
	require.NoError(t, db.Create(&models.Property{Name: "Home", Address: "1", Provider: "powerstream", IsActive: true}).Error)

	h := &handlers.UtilityBudgetHandler{DB: db}
	r := gin.New()
	r.GET("/utility-budgets", h.List)
	r.POST("/utility-budgets", h.Upsert)
	r.GET("/utility-budgets/pace", h.Pace)
	return r, h
}

func TestUtilityBudgets_Upsert_PersistsThresholdAndProperty(t *testing.T) {
	r, _ := newBudgetRouter(t)
	body := `{"month":4,"year":2026,"budget_amount":300,"alert_threshold_pct":90,"property_id":1,"currency":"CAD"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/utility-budgets", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var b models.EnergyBudget
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &b))
	require.Equal(t, 90, b.AlertThresholdPct)
	require.NotNil(t, b.PropertyID)
	require.Equal(t, uint(1), *b.PropertyID)
}

func TestUtilityBudgets_Pace_ReturnsProjection(t *testing.T) {
	r, h := newBudgetRouter(t)
	require.NoError(t, h.DB.Create(&models.UtilityBill{
		PropertyID: 1, UploadedBy: 1, FileHash: "y1", Currency: "CAD",
		PaymentStatus: "paid", IngestionSource: "manual_upload", ExtractionStatus: "completed",
		StatementDate: time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC),
		DueDate:       time.Date(2025, 4, 21, 0, 0, 0, 0, time.UTC),
		TotalAmount:   150,
	}).Error)
	pid := uint(1)
	require.NoError(t, h.DB.Create(&models.EnergyBudget{
		Month: 4, Year: 2026, BudgetAmount: 200, AlertThresholdPct: 80, Currency: "CAD", PropertyID: &pid,
	}).Error)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/utility-budgets/pace?property_id=1&month=4&year=2026", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Projection struct {
			Baseline  string  `json:"baseline"`
			Projected float64 `json:"projected"`
		} `json:"projection"`
		Budget struct {
			BudgetAmount float64 `json:"budget_amount"`
		} `json:"budget"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "yoy", resp.Projection.Baseline)
	require.InDelta(t, 150, resp.Projection.Projected, 1)
	require.InDelta(t, 200, resp.Budget.BudgetAmount, 0.01)
}
