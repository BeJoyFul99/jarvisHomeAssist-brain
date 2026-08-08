package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"jarvishomeassist-brain/internal/config"
	"jarvishomeassist-brain/internal/debt"
	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"
)

// DebtHandler serves Debt Rescue Mode: per-user storage of the financial
// inputs, deterministic plan computation, and an optional AI coaching brief
// written on top of the computed numbers. Every query is owner-scoped by the
// JWT user.
type DebtHandler struct {
	DB  *gorm.DB
	Cfg *config.Config
	Log *logger.Logger
}

// maxDebtProfileBytes caps the stored inputs. A household with hundreds of
// debts is still far under this; the cap only guards against abuse.
const maxDebtProfileBytes = 64 * 1024

// maxDebts bounds the simulation cost per request.
const maxDebts = 50

const debtCoachSystemPrompt = `You are a calm, practical debt counsellor helping someone in financial distress.

You will be given a RECOVERY PLAN as JSON. Every number in it was computed by a deterministic amortization engine and is correct.

Rules:
- NEVER invent, recompute, or contradict any figure. Quote only numbers present in the plan JSON.
- If the plan is not feasible (feasible=false) or reports a shortfall, lead with that. Do not offer an encouraging payoff timeline that does not exist. Name concrete moves: which fixed costs to cut, hardship programs, balance transfers, renegotiating minimums, raising income.
- Be direct and non-judgemental. No lectures about past spending.
- Explain WHY the recommended order works (highest rate first costs least; smallest balance first builds momentum) in one short paragraph.
- Give 3-5 specific, actionable next steps for the coming month.
- Keep the whole response under 350 words. Plain prose and short bullet lists only — no markdown headings, no tables, no code fences.`

func (h *DebtHandler) userID(c *gin.Context) (uint, bool) {
	v, _ := c.Get("user_id")
	id, ok := v.(uint)
	return id, ok
}

// planRequest is the wire shape of the planner inputs.
type planRequest struct {
	MonthlyIncome   float64 `json:"monthly_income"`
	FixedExpenses   float64 `json:"fixed_expenses"`
	AvailableCash   float64 `json:"available_cash"`
	Strategy        string  `json:"strategy"`
	PaymentOverride float64 `json:"payment_override"`
	Currency        string  `json:"currency"`
	Debts           []struct {
		Name       string  `json:"name"`
		Balance    float64 `json:"balance"`
		APR        float64 `json:"apr"`
		MinPayment float64 `json:"min_payment"`
	} `json:"debts"`
}

// toInput converts the request into engine input, rejecting values that would
// make the simulation meaningless.
func (r planRequest) toInput() (debt.Input, error) {
	if r.MonthlyIncome < 0 || r.FixedExpenses < 0 || r.AvailableCash < 0 {
		return debt.Input{}, fmt.Errorf("income, expenses and cash cannot be negative")
	}
	if len(r.Debts) > maxDebts {
		return debt.Input{}, fmt.Errorf("too many debts (limit %d)", maxDebts)
	}

	in := debt.Input{
		MonthlyIncome:   r.MonthlyIncome,
		FixedExpenses:   r.FixedExpenses,
		AvailableCash:   r.AvailableCash,
		Strategy:        r.Strategy,
		PaymentOverride: r.PaymentOverride,
		Currency:        r.Currency,
		StartDate:       time.Now(),
	}
	for i, d := range r.Debts {
		if d.Balance < 0 || d.MinPayment < 0 || d.APR < 0 {
			return debt.Input{}, fmt.Errorf("debt %d has a negative balance, rate or minimum", i+1)
		}
		if d.APR > 1000 {
			return debt.Input{}, fmt.Errorf("debt %d has an implausible interest rate", i+1)
		}
		name := strings.TrimSpace(d.Name)
		if name == "" {
			name = fmt.Sprintf("Debt %d", i+1)
		}
		in.Debts = append(in.Debts, debt.Debt{
			Name:       name,
			Balance:    d.Balance,
			APR:        d.APR,
			MinPayment: d.MinPayment,
		})
	}
	return in, nil
}

// GetProfile returns the current user's saved Debt Rescue inputs.
// GET /api/v1/debt/profile
func (h *DebtHandler) GetProfile(c *gin.Context) {
	uid, ok := h.userID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var profile models.DebtProfile
	if err := h.DB.WithContext(ctx).Where("user_id = ?", uid).Take(&profile).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"data": gin.H{}, "updated_at": nil})
		return
	}

	var data map[string]interface{}
	if profile.Data != nil {
		_ = json.Unmarshal(profile.Data, &data)
	}
	if data == nil {
		data = map[string]interface{}{}
	}
	c.JSON(http.StatusOK, gin.H{"data": data, "updated_at": profile.UpdatedAt})
}

// UpdateProfile replaces the current user's saved Debt Rescue inputs.
// PUT /api/v1/debt/profile
func (h *DebtHandler) UpdateProfile(c *gin.Context) {
	uid, ok := h.userID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxDebtProfileBytes+1))
	if err != nil || len(body) > maxDebtProfileBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile too large"})
		return
	}
	if !json.Valid(body) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	profile := models.DebtProfile{UserID: uid, Data: datatypes.JSON(body)}
	if err := h.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"data", "updated_at"}),
	}).Create(&profile).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save profile"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": json.RawMessage(body), "updated_at": profile.UpdatedAt})
}

// Plan computes the recovery plan from the posted inputs. Pure arithmetic —
// no AI, no persistence, instant.
// POST /api/v1/debt/plan
func (h *DebtHandler) Plan(c *gin.Context) {
	if _, ok := h.userID(c); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req planRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": "Could not read the financial details"})
		return
	}

	in, err := req.toInput()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"plan": debt.Build(in)})
}

// Coach asks the AI worker for a narrative brief built strictly on top of the
// deterministically computed plan.
// POST /api/v1/debt/coach
func (h *DebtHandler) Coach(c *gin.Context) {
	if _, ok := h.userID(c); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if h.Cfg == nil || h.Cfg.CFWorkerURL == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ai_unavailable", "message": "AI worker is not configured"})
		return
	}

	var req struct {
		planRequest
		Model string `json:"model"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": "Could not read the financial details"})
		return
	}

	in, err := req.planRequest.toInput()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "validation_error", "message": err.Error()})
		return
	}

	// The model never sees raw inputs — only the verified plan, so it has
	// nothing to recompute and nothing to get wrong.
	plan := debt.Build(in)
	planJSON, _ := json.Marshal(plan)

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = GetSetting(h.DB, "ai_debt_model", "")
	}

	chatReq := map[string]interface{}{
		"messages": []map[string]string{
			{"role": "system", "content": debtCoachSystemPrompt},
			{"role": "user", "content": "RECOVERY PLAN (JSON):\n" + string(planJSON)},
		},
		"tags":       []string{"debt"},
		"max_tokens": 1200,
	}
	if model != "" {
		chatReq["model"] = model
	}
	reqBody, _ := json.Marshal(chatReq)

	client := &http.Client{Timeout: 120 * time.Second}
	httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, h.Cfg.CFWorkerURL+"/v1/chat", bytes.NewReader(reqBody))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build AI request"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+h.Cfg.CFWorkerSecret)

	resp, err := client.Do(httpReq)
	if err != nil {
		h.logError(fmt.Sprintf("worker request: %v", err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai_unreachable", "message": "AI worker is unreachable"})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode == http.StatusTooManyRequests {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited", "message": "Daily AI limit reached — try again tomorrow"})
		return
	}
	if resp.StatusCode != http.StatusOK {
		h.logError(fmt.Sprintf("worker returned %d: %s", resp.StatusCode, string(respBody)))
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai_error", "message": "AI worker returned an error"})
		return
	}

	var workerResp struct {
		Response string `json:"response"`
	}
	brief := string(respBody)
	if json.Unmarshal(respBody, &workerResp) == nil && workerResp.Response != "" {
		brief = workerResp.Response
	}

	c.JSON(http.StatusOK, gin.H{"plan": plan, "brief": strings.TrimSpace(brief), "model": model})
}

func (h *DebtHandler) logError(msg string) {
	if h.Log != nil {
		h.Log.Error("debt", msg)
	}
}
