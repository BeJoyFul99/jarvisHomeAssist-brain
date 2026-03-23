package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"jarvishomeassist-brain/internal/config"
)

// AIUsageHandler serves AI usage analytics from Cloudflare Analytics Engine.
type AIUsageHandler struct {
	Cfg *config.Config
}

// analyticsResponse matches the Cloudflare Analytics Engine SQL API response.
type analyticsResponse struct {
	Data                   []map[string]interface{} `json:"data"`
	Rows                   int                      `json:"rows"`
	RowsBeforeLimitAtLeast int                      `json:"rows_before_limit_at_least"`
}

// queryAnalyticsEngine executes a SQL query against the Cloudflare Analytics Engine API.
func (h *AIUsageHandler) queryAnalyticsEngine(query string) (*analyticsResponse, error) {
	if h.Cfg.CFAccountID == "" || h.Cfg.CFAPIToken == "" {
		return nil, fmt.Errorf("CF_ACCOUNT_ID and CF_API_TOKEN required")
	}

	url := fmt.Sprintf(
		"https://api.cloudflare.com/client/v4/accounts/%s/analytics_engine/sql",
		h.Cfg.CFAccountID,
	)

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("POST", url, strings.NewReader(query))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.Cfg.CFAPIToken)
	req.Header.Set("Content-Type", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("analytics API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	log.Printf("[ai-usage] CF Analytics response (%d): %s", resp.StatusCode, string(body))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("analytics API error (%d): %s", resp.StatusCode, string(body))
	}

	var result analyticsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w, body: %s", err, string(body))
	}

	return &result, nil
}

// Summary returns aggregated AI usage data.
// GET /api/v1/admin/ai-usage/summary?days=7
func (h *AIUsageHandler) Summary(c *gin.Context) {
	days := c.DefaultQuery("days", "7")

	query := fmt.Sprintf(`
		SELECT
			toDate(timestamp) as day,
			COUNT() as total_calls,
			SUM(double1) as total_neurons,
			SUM(double2) as total_cost,
			AVG(double3) as avg_latency_ms,
			SUM(double4) as total_input_tokens,
			SUM(double5) as total_output_tokens
		FROM jarvis_home_assist_analytics
		WHERE timestamp > NOW() - INTERVAL '%s' DAY
		GROUP BY day
		ORDER BY day DESC
	`, days)

	result, err := h.queryAnalyticsEngine(query)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"error":   err.Error(),
			"days":    []interface{}{},
			"message": "Analytics Engine not configured or unavailable.",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"days": result.Data, "total_rows": result.Rows})
}

// ByModel returns usage broken down by model.
// GET /api/v1/admin/ai-usage/by-model?days=7
func (h *AIUsageHandler) ByModel(c *gin.Context) {
	days := c.DefaultQuery("days", "7")

	query := fmt.Sprintf(`
		SELECT
			blob1 as model,
			blob6 as content_type,
			COUNT() as total_calls,
			SUM(double1) as total_neurons,
			SUM(double2) as total_cost,
			AVG(double3) as avg_latency_ms
		FROM jarvis_home_assist_analytics
		WHERE timestamp > NOW() - INTERVAL '%s' DAY
		GROUP BY model, content_type
		ORDER BY total_neurons DESC
	`, days)

	result, err := h.queryAnalyticsEngine(query)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"error": err.Error(), "models": []interface{}{}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"models": result.Data, "total_rows": result.Rows})
}

// Errors returns recent AI errors.
// GET /api/v1/admin/ai-usage/errors?days=1
func (h *AIUsageHandler) Errors(c *gin.Context) {
	days := c.DefaultQuery("days", "1")

	query := fmt.Sprintf(`
		SELECT
			timestamp,
			blob1 as model,
			blob2 as task,
			blob5 as error_message,
			double3 as latency_ms
		FROM jarvis_home_assist_analytics
		WHERE timestamp > NOW() - INTERVAL '%s' DAY
			AND blob3 = 'error'
		ORDER BY timestamp DESC
		LIMIT 50
	`, days)

	result, err := h.queryAnalyticsEngine(query)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"error": err.Error(), "errors": []interface{}{}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"errors": result.Data, "total_rows": result.Rows})
}

// Today returns today's usage for the dashboard header/alert.
// GET /api/v1/admin/ai-usage/today
func (h *AIUsageHandler) Today(c *gin.Context) {
	query := `
		SELECT
			COUNT() as total_calls,
			SUM(double1) as total_neurons,
			SUM(double2) as total_cost,
			AVG(double3) as avg_latency_ms,
			countIf(blob3 = 'error') as error_count,
			countIf(blob6 = 'vision') as vision_calls
		FROM jarvis_home_assist_analytics
		WHERE timestamp > NOW() - INTERVAL '1' DAY
	`

	result, err := h.queryAnalyticsEngine(query)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"total_calls":    0,
			"total_neurons":  0,
			"total_cost":     0,
			"avg_latency_ms": 0,
			"error_count":    0,
			"vision_calls":   0,
			"free_tier_limit": 10000,
			"error":          err.Error(),
		})
		return
	}

	data := gin.H{
		"free_tier_limit": 10000,
	}
	if len(result.Data) > 0 {
		for k, v := range result.Data[0] {
			data[k] = v
		}
	}

	// Add alert level
	neurons := toFloat(data["total_neurons"])
	if neurons >= 10000 {
		data["alert"] = "critical"
		data["alert_message"] = "Free tier limit reached! AI calls may be throttled."
	} else if neurons >= 8000 {
		data["alert"] = "warning"
		data["alert_message"] = fmt.Sprintf("Approaching free tier limit: %.0f/10,000 neurons used today", neurons)
	}

	c.JSON(http.StatusOK, data)
}

// Config returns the AI worker usage tracking configuration.
// GET /api/v1/admin/ai-usage/config
func (h *AIUsageHandler) Config(c *gin.Context) {
	if h.Cfg.CFWorkerURL == "" {
		c.JSON(http.StatusOK, gin.H{"error": "CF_WORKER_URL not configured"})
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", h.Cfg.CFWorkerURL+"/v1/usage/config", nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "worker unreachable"})
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var respData interface{}
	json.Unmarshal(body, &respData)
	c.JSON(resp.StatusCode, respData)
}

// toFloat safely converts interface{} to float64 (handles string numbers from CF API).
func toFloat(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case string:
		var f float64
		fmt.Sscanf(val, "%f", &f)
		return f
	case json.Number:
		f, _ := val.Float64()
		return f
	}
	return 0
}
