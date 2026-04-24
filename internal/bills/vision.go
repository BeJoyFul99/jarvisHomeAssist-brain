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

// VisionClient calls the Cloudflare Worker AI endpoint with rasterized PDF
// pages and asks it to return structured bill JSON. Spec §3.3.
type VisionClient struct {
	workerURL string
	secret    string
	http      *http.Client
}

// NewVisionClient is wired with cfg.CFWorkerURL + cfg.CFWorkerSecret at runtime.
func NewVisionClient(workerURL, secret string, timeout time.Duration) *VisionClient {
	return &VisionClient{
		workerURL: workerURL,
		secret:    secret,
		http:      &http.Client{Timeout: timeout},
	}
}

const visionSystemPrompt = `You are a utility-bill extraction assistant. The attached images are pages of a PowerStream Energy Services bill (electricity, water, HVAC). Return ONLY a JSON object with these top-level keys and no prose:
{
  "account_number": string, "service_address": string,
  "statement_date": "YYYY-MM-DD", "due_date": "YYYY-MM-DD",
  "billing_period_start": "YYYY-MM-DD", "billing_period_end": "YYYY-MM-DD",
  "bill_type": "REGULAR" | "ESTIMATED" | "FINAL",
  "total_amount": number, "previous_balance": number, "payments_received": number,
  "balance_forward": number, "late_fees": number, "currency": "CAD",
  "line_items": [{"utility_type": "electricity"|"water"|"hvac"|"other",
                  "category": "energy"|"delivery"|"regulatory"|"hst"|"rebate"|"late_fee"|"debt_retirement",
                  "description": string, "usage_amount": number|null, "usage_unit": string|null,
                  "rate": number|null, "amount": number}],
  "meters": [{"meter_type": "electric"|"water"|"hvac", "meter_number": string,
              "previous_reading": string, "current_reading": string,
              "usage": number, "multiplier": number}]
}
If a field is unknown, omit it. Do not invent data.`

// Extract posts page images to the worker and parses the JSON response.
func (c *VisionClient) Extract(ctx context.Context, pages [][]byte) (ParsedBill, error) {
	if len(pages) == 0 {
		return ParsedBill{}, errors.New("no pages to extract")
	}
	type imagePart struct {
		Type     string `json:"type"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	type textPart struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	parts := make([]any, 0, len(pages)+1)
	parts = append(parts, textPart{Type: "text", Text: "Extract the bill into JSON per the system prompt."})
	for _, png := range pages {
		img := imagePart{Type: "image_url"}
		img.ImageURL.URL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		parts = append(parts, img)
	}
	body := map[string]any{
		"messages": []message{
			{Role: "system", Content: visionSystemPrompt},
			{Role: "user", Content: parts},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return ParsedBill{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.workerURL+"/v1/chat", bytes.NewReader(raw))
	if err != nil {
		return ParsedBill{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.secret)

	resp, err := c.http.Do(req)
	if err != nil {
		return ParsedBill{}, fmt.Errorf("vision request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return ParsedBill{}, fmt.Errorf("vision worker returned %d", resp.StatusCode)
	}

	var wrapper struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return ParsedBill{}, fmt.Errorf("decode wrapper: %w", err)
	}

	var raw2 struct {
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
			MeterType       string  `json:"meter_type"`
			MeterNumber     string  `json:"meter_number"`
			PreviousReading string  `json:"previous_reading"`
			CurrentReading  string  `json:"current_reading"`
			Usage           float64 `json:"usage"`
			Multiplier      int     `json:"multiplier"`
		} `json:"meters"`
	}
	if err := json.Unmarshal([]byte(wrapper.Content), &raw2); err != nil {
		return ParsedBill{}, fmt.Errorf("vision json: %w", err)
	}
	pb := ParsedBill{
		AccountNumber:    raw2.AccountNumber,
		ServiceAddress:   raw2.ServiceAddress,
		BillType:         raw2.BillType,
		TotalAmount:      raw2.TotalAmount,
		PreviousBalance:  raw2.PreviousBalance,
		PaymentsReceived: raw2.PaymentsReceived,
		BalanceForward:   raw2.BalanceForward,
		LateFees:         raw2.LateFees,
		Currency:         firstOr(raw2.Currency, "CAD"),
	}
	parseDate := func(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }
	pb.StatementDate = parseDate(raw2.StatementDate)
	pb.DueDate = parseDate(raw2.DueDate)
	pb.BillingPeriodStart = parseDate(raw2.BillingPeriodStart)
	pb.BillingPeriodEnd = parseDate(raw2.BillingPeriodEnd)
	for _, li := range raw2.LineItems {
		pb.LineItems = append(pb.LineItems, ParsedLineItem{
			UtilityType: li.UtilityType, Category: li.Category, Description: li.Description,
			UsageAmount: li.UsageAmount, UsageUnit: li.UsageUnit, Rate: li.Rate, Amount: li.Amount,
		})
	}
	for _, m := range raw2.Meters {
		pb.Meters = append(pb.Meters, ParsedMeter{
			MeterType: m.MeterType, MeterNumber: m.MeterNumber,
			PreviousReading: m.PreviousReading, CurrentReading: m.CurrentReading,
			Usage: m.Usage, Multiplier: m.Multiplier,
		})
	}
	return pb, nil
}

func firstOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
