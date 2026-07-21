package bills

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// VisionClient calls the Cloudflare Worker's /v1/bills/extract endpoint with
// rasterized PDF pages and receives a structured bill + model-self-reported
// confidence. Spec §3.3.
type VisionClient struct {
	workerURL string
	secret    string
	http      *http.Client
	// ModelProvider, when set, supplies the admin-selected extraction model
	// per request (read from settings). Empty string = worker default.
	ModelProvider func() string
}

// NewVisionClient is wired with cfg.CFWorkerURL + cfg.CFWorkerSecret at runtime.
func NewVisionClient(workerURL, secret string, timeout time.Duration) *VisionClient {
	return &VisionClient{
		workerURL: workerURL,
		secret:    secret,
		http:      &http.Client{Timeout: timeout},
	}
}

// visionResponse matches the shape returned by POST /v1/bills/extract.
type visionResponse struct {
	Model string `json:"model"`
	Data  struct {
		AccountNumber      string  `json:"account_number"`
		ServiceAddress     string  `json:"service_address"`
		StatementDate      string  `json:"statement_date"`
		DueDate            string  `json:"due_date"`
		BillingPeriodStart string  `json:"billing_period_start"`
		BillingPeriodEnd   string  `json:"billing_period_end"`
		BillType           string  `json:"bill_type"`
		TotalAmount        float64 `json:"total_amount"`
		PreviousBalance    float64 `json:"previous_balance"`
		PaymentsReceived   float64 `json:"payments_received"`
		BalanceForward     float64 `json:"balance_forward"`
		LateFees           float64 `json:"late_fees"`
		Currency           string  `json:"currency"`
		LineItems          []struct {
			UtilityType string   `json:"utility_type"`
			Category    string   `json:"category"`
			Description string   `json:"description"`
			UsageAmount *float64 `json:"usage_amount"`
			UsageUnit   *string  `json:"usage_unit"`
			Rate        *float64 `json:"rate"`
			Amount      float64  `json:"amount"`
		} `json:"line_items"`
		Meters []struct {
			MeterType        string  `json:"meter_type"`
			MeterNumber      string  `json:"meter_number"`
			PreviousReading  string  `json:"previous_reading"`
			PreviousReadDate string  `json:"previous_read_date"`
			CurrentReading   string  `json:"current_reading"`
			CurrentReadDate  string  `json:"current_read_date"`
			Usage            float64 `json:"usage"`
			Multiplier       int     `json:"multiplier"`
		} `json:"meters"`
	} `json:"data"`
	Confidence int    `json:"confidence"`
	Notes      string `json:"notes"`
}

// Extract posts page images to the worker's dedicated bill-extract endpoint
// and decodes the structured response. Returns (parsed bill, AI-reported
// confidence 0-100, model handle, error).
func (c *VisionClient) Extract(ctx context.Context, pages [][]byte) (ParsedBill, int, string, error) {
	if len(pages) == 0 {
		return ParsedBill{}, 0, "", errors.New("no pages to extract")
	}

	b64pages := make([]string, len(pages))
	for i, png := range pages {
		b64pages[i] = base64.StdEncoding.EncodeToString(png)
	}
	body := map[string]any{"pages": b64pages, "tags": []string{"bill_extract"}}
	if c.ModelProvider != nil {
		if m := c.ModelProvider(); m != "" {
			body["model"] = m
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return ParsedBill{}, 0, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.workerURL+"/v1/bills/extract", bytes.NewReader(raw))
	if err != nil {
		return ParsedBill{}, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.secret)

	resp, err := c.http.Do(req)
	if err != nil {
		return ParsedBill{}, 0, "", fmt.Errorf("vision request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := readAllLimited(resp.Body, 4096)
		// Try to surface the model from the response header even on error.
		model := resp.Header.Get("X-AI-Model")
		return ParsedBill{}, 0, model, fmt.Errorf("vision worker returned %d: %s", resp.StatusCode, string(b))
	}

	var vr visionResponse
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return ParsedBill{}, 0, "", fmt.Errorf("decode response: %w", err)
	}

	parseDate := func(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }
	pb := ParsedBill{
		AccountNumber:      vr.Data.AccountNumber,
		ServiceAddress:     vr.Data.ServiceAddress,
		StatementDate:      parseDate(vr.Data.StatementDate),
		DueDate:            parseDate(vr.Data.DueDate),
		BillingPeriodStart: parseDate(vr.Data.BillingPeriodStart),
		BillingPeriodEnd:   parseDate(vr.Data.BillingPeriodEnd),
		BillType:           vr.Data.BillType,
		TotalAmount:        vr.Data.TotalAmount,
		PreviousBalance:    vr.Data.PreviousBalance,
		PaymentsReceived:   vr.Data.PaymentsReceived,
		BalanceForward:     vr.Data.BalanceForward,
		LateFees:           vr.Data.LateFees,
		Currency:           firstOr(vr.Data.Currency, "CAD"),
	}
	for _, li := range vr.Data.LineItems {
		pb.LineItems = append(pb.LineItems, ParsedLineItem{
			UtilityType: li.UtilityType, Category: li.Category, Description: li.Description,
			UsageAmount: li.UsageAmount, UsageUnit: li.UsageUnit, Rate: li.Rate, Amount: li.Amount,
		})
	}
	for _, m := range vr.Data.Meters {
		pb.Meters = append(pb.Meters, ParsedMeter{
			MeterType:        m.MeterType,
			MeterNumber:      m.MeterNumber,
			PreviousReading:  m.PreviousReading,
			PreviousReadDate: parseDate(m.PreviousReadDate),
			CurrentReading:   m.CurrentReading,
			CurrentReadDate:  parseDate(m.CurrentReadDate),
			Usage:            m.Usage,
			Multiplier:       m.Multiplier,
		})
	}
	return pb, vr.Confidence, vr.Model, nil
}

func firstOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func readAllLimited(r interface{ Read(p []byte) (int, error) }, max int) ([]byte, error) {
	buf := make([]byte, max)
	n, err := r.Read(buf)
	if err != nil && n == 0 {
		return nil, err
	}
	return buf[:n], nil
}
