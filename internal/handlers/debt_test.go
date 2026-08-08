package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/config"
	"jarvishomeassist-brain/internal/handlers"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

// newDebtRouter wires the debt handler behind a stub auth middleware that reads
// the acting user from the X-Test-User header, mirroring what JWTAuth provides
// via c.Set("user_id").
func newDebtRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.DebtProfile{}, &models.Setting{}))
	h := &handlers.DebtHandler{DB: db, Cfg: &config.Config{}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		uid, _ := strconv.Atoi(c.GetHeader("X-Test-User"))
		c.Set("user_id", uint(uid))
		c.Next()
	})
	r.GET("/debt/profile", h.GetProfile)
	r.PUT("/debt/profile", h.UpdateProfile)
	r.POST("/debt/plan", h.Plan)
	r.POST("/debt/coach", h.Coach)
	return r, db
}

func doDebt(r *gin.Engine, method, path, user, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var req *http.Request
	if body != "" {
		req, _ = http.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, path, nil)
	}
	req.Header.Set("X-Test-User", user)
	r.ServeHTTP(w, req)
	return w
}

// debtPlanResponse mirrors the fields the UI depends on.
type debtPlanResponse struct {
	Plan struct {
		Feasible             bool    `json:"feasible"`
		Strategy             string  `json:"strategy"`
		TotalDebt            float64 `json:"total_debt"`
		MonthlyPaymentTarget float64 `json:"monthly_payment_target"`
		MonthsToFreedom      int     `json:"months_to_freedom"`
		DebtFreeDate         string  `json:"debt_free_date"`
		InterestSaved        float64 `json:"interest_saved"`
		Summary              string  `json:"summary"`
		Survival             struct {
			Disposable float64 `json:"disposable"`
			Shortfall  float64 `json:"shortfall"`
		} `json:"survival"`
		Order []struct {
			Position int    `json:"position"`
			Name     string `json:"name"`
		} `json:"order"`
		Warnings []string `json:"warnings"`
	} `json:"plan"`
}

func TestDebtPlan_ProducesFullRecoveryPlan(t *testing.T) {
	r, _ := newDebtRouter(t)

	w := doDebt(r, http.MethodPost, "/debt/plan", "1", `{
		"monthly_income": 3200,
		"fixed_expenses": 2400,
		"available_cash": 0,
		"strategy": "avalanche",
		"debts": [
			{"name": "Credit Card", "balance": 4000, "apr": 19.99, "min_payment": 120},
			{"name": "Personal Loan", "balance": 2250, "apr": 8.5, "min_payment": 90}
		]
	}`)
	require.Equal(t, http.StatusOK, w.Code)

	var resp debtPlanResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	require.True(t, resp.Plan.Feasible)
	require.Equal(t, 6250.0, resp.Plan.TotalDebt)
	require.Equal(t, "avalanche", resp.Plan.Strategy)
	require.InDelta(t, 741.0, resp.Plan.MonthlyPaymentTarget, 0.01)
	require.InDelta(t, 9, resp.Plan.MonthsToFreedom, 1)
	require.NotEmpty(t, resp.Plan.DebtFreeDate, "debt-free date must be present")
	require.Greater(t, resp.Plan.InterestSaved, 0.0)
	require.NotEmpty(t, resp.Plan.Summary)

	// Avalanche puts the 19.99% card ahead of the 8.5% loan.
	require.Len(t, resp.Plan.Order, 2)
	require.Equal(t, "Credit Card", resp.Plan.Order[0].Name)
	require.Equal(t, 1, resp.Plan.Order[0].Position)
}

func TestDebtPlan_ShortfallIsSurfaced(t *testing.T) {
	r, _ := newDebtRouter(t)

	w := doDebt(r, http.MethodPost, "/debt/plan", "1", `{
		"monthly_income": 1500,
		"fixed_expenses": 1400,
		"debts": [{"name": "Card", "balance": 5000, "apr": 22, "min_payment": 240}]
	}`)
	require.Equal(t, http.StatusOK, w.Code, "an unaffordable situation is a valid plan, not an error")

	var resp debtPlanResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	require.False(t, resp.Plan.Feasible)
	require.InDelta(t, 140.0, resp.Plan.Survival.Shortfall, 0.01)
	require.Empty(t, resp.Plan.DebtFreeDate, "must not invent a payoff date it can't reach")
	require.NotEmpty(t, resp.Plan.Warnings)
}

func TestDebtPlan_RejectsInvalidInput(t *testing.T) {
	r, _ := newDebtRouter(t)

	cases := map[string]string{
		"negative income":  `{"monthly_income": -100, "fixed_expenses": 0, "debts": []}`,
		"negative balance": `{"monthly_income": 3000, "fixed_expenses": 1000, "debts": [{"name": "X", "balance": -5, "apr": 10, "min_payment": 5}]}`,
		"absurd apr":       `{"monthly_income": 3000, "fixed_expenses": 1000, "debts": [{"name": "X", "balance": 100, "apr": 99999, "min_payment": 5}]}`,
		"malformed json":   `{"monthly_income":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			w := doDebt(r, http.MethodPost, "/debt/plan", "1", body)
			require.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestDebtPlan_UnnamedDebtsGetLabels(t *testing.T) {
	r, _ := newDebtRouter(t)

	w := doDebt(r, http.MethodPost, "/debt/plan", "1", `{
		"monthly_income": 4000,
		"fixed_expenses": 1000,
		"debts": [{"name": "  ", "balance": 1000, "apr": 10, "min_payment": 50}]
	}`)
	require.Equal(t, http.StatusOK, w.Code)

	var resp debtPlanResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Plan.Order, 1)
	require.Equal(t, "Debt 1", resp.Plan.Order[0].Name)
}

func TestDebtProfile_RoundTripAndOwnerScoping(t *testing.T) {
	r, _ := newDebtRouter(t)

	body := `{"monthly_income":3200,"fixed_expenses":2400,"debts":[{"name":"Card","balance":4000,"apr":19.99,"min_payment":120}]}`
	w := doDebt(r, http.MethodPut, "/debt/profile", "1", body)
	require.Equal(t, http.StatusOK, w.Code)

	w = doDebt(r, http.MethodGet, "/debt/profile", "1", "")
	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, 3200.0, got.Data["monthly_income"])

	// User 2 sees an empty profile — never user 1's finances. Decode into a
	// fresh value: unmarshalling into the populated `got` would merge rather
	// than reset the map and silently pass.
	w = doDebt(r, http.MethodGet, "/debt/profile", "2", "")
	require.Equal(t, http.StatusOK, w.Code)
	var other struct {
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &other))
	require.Empty(t, other.Data)
}

func TestDebtProfile_UpsertsInsteadOfDuplicating(t *testing.T) {
	r, db := newDebtRouter(t)

	require.Equal(t, http.StatusOK,
		doDebt(r, http.MethodPut, "/debt/profile", "1", `{"monthly_income":3000}`).Code)
	require.Equal(t, http.StatusOK,
		doDebt(r, http.MethodPut, "/debt/profile", "1", `{"monthly_income":4000}`).Code)

	var count int64
	require.NoError(t, db.Model(&models.DebtProfile{}).Where("user_id = ?", 1).Count(&count).Error)
	require.EqualValues(t, 1, count, "saving twice must update one row, not insert a second")

	w := doDebt(r, http.MethodGet, "/debt/profile", "1", "")
	var got struct {
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, 4000.0, got.Data["monthly_income"], "latest save wins")
}

func TestDebtProfile_RejectsInvalidJSON(t *testing.T) {
	r, _ := newDebtRouter(t)
	w := doDebt(r, http.MethodPut, "/debt/profile", "1", `not json at all`)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// With no AI worker configured, coaching degrades to a clear 503 rather than
// hanging or crashing — the deterministic plan endpoint still works.
func TestDebtCoach_UnavailableWithoutWorker(t *testing.T) {
	r, _ := newDebtRouter(t)
	w := doDebt(r, http.MethodPost, "/debt/coach", "1",
		`{"monthly_income":3000,"fixed_expenses":1000,"debts":[]}`)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "ai_unavailable")
}
