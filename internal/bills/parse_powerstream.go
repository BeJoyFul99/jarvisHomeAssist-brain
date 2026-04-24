package bills

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParsedBill is the domain object produced by ParsePowerStream and consumed by
// the orchestrator to populate UtilityBill / LineItem / Meter rows.
type ParsedBill struct {
	AccountNumber      string
	ServiceAddress     string
	StatementDate      time.Time
	DueDate            time.Time
	BillingPeriodStart time.Time
	BillingPeriodEnd   time.Time
	BillType           string

	TotalAmount      float64
	PreviousBalance  float64
	PaymentsReceived float64
	BalanceForward   float64
	LateFees         float64
	Currency         string

	LineItems []ParsedLineItem
	Meters    []ParsedMeter
}

// ParsedLineItem represents a single charge line extracted from the bill.
type ParsedLineItem struct {
	UtilityType string
	Category    string
	Description string
	UsageAmount *float64
	UsageUnit   *string
	Rate        *float64
	Amount      float64
}

// ParsedMeter represents a meter reading block extracted from the bill.
type ParsedMeter struct {
	MeterType        string
	MeterNumber      string
	PreviousReading  string
	PreviousReadDate time.Time
	CurrentReading   string
	CurrentReadDate  time.Time
	Usage            float64
	Multiplier       int
}

// LineItemsByType filters line items to a single utility type.
func (p ParsedBill) LineItemsByType(t string) []ParsedLineItem {
	out := make([]ParsedLineItem, 0)
	for _, li := range p.LineItems {
		if li.UtilityType == t {
			out = append(out, li)
		}
	}
	return out
}

var (
	reAccount   = regexp.MustCompile(`(?im)^Account Number:\s*(\S+)`)
	reAddress   = regexp.MustCompile(`(?im)^Service Address:\s*(.+)$`)
	reStatement = regexp.MustCompile(`(?im)^Statement Date:\s*(\d{4}-\d{2}-\d{2})`)
	reDue       = regexp.MustCompile(`(?im)^Due Date:\s*(\d{4}-\d{2}-\d{2})`)
	reBillType  = regexp.MustCompile(`(?im)^Bill Type:\s*(\S+)`)
	reTotal     = regexp.MustCompile(`(?im)^TOTAL CURRENT CHARGES\s+\$?([-\d.,]+)`)
	rePrevBal   = regexp.MustCompile(`(?im)^PREVIOUS BALANCE\s+\$?([-\d.,]+)`)
	rePayment   = regexp.MustCompile(`(?im)^PAYMENT\s+[-\d/]+\s+-\$?([-\d.,]+)`)
	reBalFwd    = regexp.MustCompile(`(?im)^BALANCE FORWARD\s+\$?([-\d.,]+)`)
	reLateFee   = regexp.MustCompile(`(?im)^Late Fee\s+\$?([-\d.,]+)`)
	// Line item: description, optional "<n> kWh|m3", optional "@ $<rate>", sign, amount.
	reLineItem = regexp.MustCompile(`(?m)^\s*(.+?)\s+(\d+\s+(?:kWh|m3))?\s*(?:@\s*\$?([\d.]+))?\s+(-?)\$?([\d.,]+)\s*$`)
	reMeter    = regexp.MustCompile(`(?im)^(Electric|Water|HVAC)\s+(\S+)\s+Prev:\s*(\S+)\s+Curr:\s*(\S+)\s+Usage:\s*([\d.]+)\s+(\w+)\s+Multiplier:\s*(\d+)`)
)

// ParsePowerStream applies template regexes to raw bill text and returns a
// ParsedBill. Missing fields are left at zero values; the caller
// (ScoreConfidence in Task 14) decides whether the gaps are acceptable.
func ParsePowerStream(text string) (ParsedBill, error) {
	p := ParsedBill{Currency: "CAD"}

	if m := reAccount.FindStringSubmatch(text); len(m) == 2 {
		p.AccountNumber = strings.TrimSpace(m[1])
	}
	if m := reAddress.FindStringSubmatch(text); len(m) == 2 {
		p.ServiceAddress = strings.TrimSpace(m[1])
	}
	if m := reStatement.FindStringSubmatch(text); len(m) == 2 {
		if t, err := time.Parse("2006-01-02", m[1]); err == nil {
			p.StatementDate = t
		}
	}
	if m := reDue.FindStringSubmatch(text); len(m) == 2 {
		if t, err := time.Parse("2006-01-02", m[1]); err == nil {
			p.DueDate = t
		}
	}
	if m := reBillType.FindStringSubmatch(text); len(m) == 2 {
		p.BillType = strings.TrimSpace(m[1])
	}
	if m := reTotal.FindStringSubmatch(text); len(m) == 2 {
		p.TotalAmount = parseAmount(m[1])
	}
	if m := rePrevBal.FindStringSubmatch(text); len(m) == 2 {
		p.PreviousBalance = parseAmount(m[1])
	}
	if m := rePayment.FindStringSubmatch(text); len(m) == 2 {
		p.PaymentsReceived = -parseAmount(m[1])
	}
	if m := reBalFwd.FindStringSubmatch(text); len(m) == 2 {
		p.BalanceForward = parseAmount(m[1])
	}
	if m := reLateFee.FindStringSubmatch(text); len(m) == 2 {
		p.LateFees = parseAmount(m[1])
	}

	p.LineItems = parseLineItems(text)
	p.Meters = parseMeters(text)
	return p, nil
}

func parseAmount(s string) float64 {
	s = strings.ReplaceAll(s, ",", "")
	s = strings.TrimSpace(s)
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func parseLineItems(text string) []ParsedLineItem {
	lines := strings.Split(text, "\n")
	var items []ParsedLineItem
	section := ""

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "ELECTRICITY CHARGES"):
			section = "electricity"
			continue
		case strings.HasPrefix(upper, "WATER CHARGES"):
			section = "water"
			continue
		case strings.HasPrefix(upper, "HVAC CHARGES"):
			section = "hvac"
			continue
		case strings.HasPrefix(upper, "OTHER CHARGES"):
			section = "other"
			continue
		case strings.HasPrefix(upper, "TOTAL CURRENT CHARGES"),
			strings.HasPrefix(upper, "BILL AT A GLANCE"),
			strings.HasPrefix(upper, "METER READINGS"):
			section = ""
			continue
		}
		if section == "" || line == "" {
			continue
		}
		m := reLineItem.FindStringSubmatch(line)
		if len(m) == 0 {
			continue
		}
		desc := strings.TrimSpace(m[1])
		amount := parseAmount(m[5])
		if m[4] == "-" {
			amount = -amount
		}
		li := ParsedLineItem{
			UtilityType: section,
			Category:    classifyCategory(desc, section),
			Description: desc,
			Amount:      amount,
		}
		if m[2] != "" {
			parts := strings.Fields(m[2])
			if len(parts) == 2 {
				if v, err := strconv.ParseFloat(parts[0], 64); err == nil {
					li.UsageAmount = &v
					unit := parts[1]
					li.UsageUnit = &unit
				}
			}
		}
		if m[3] != "" {
			if v, err := strconv.ParseFloat(m[3], 64); err == nil {
				li.Rate = &v
			}
		}
		items = append(items, li)
	}
	return items
}

func classifyCategory(desc, section string) string {
	d := strings.ToLower(desc)
	switch {
	case strings.Contains(d, "rebate"):
		return "rebate"
	case strings.Contains(d, "late fee"):
		return "late_fee"
	case strings.Contains(d, "debt"):
		return "debt_retirement"
	case strings.Contains(d, "hst"), strings.Contains(d, "gst"):
		return "hst"
	case strings.Contains(d, "regulatory"):
		return "regulatory"
	case strings.Contains(d, "delivery"):
		return "delivery"
	case strings.Contains(d, "tier"), strings.Contains(d, "usage"),
		strings.Contains(d, "on-peak"), strings.Contains(d, "off-peak"),
		strings.Contains(d, "natural gas"):
		return "energy"
	}
	if section == "other" {
		return "late_fee"
	}
	return "energy"
}

func parseMeters(text string) []ParsedMeter {
	var meters []ParsedMeter
	for _, m := range reMeter.FindAllStringSubmatch(text, -1) {
		if len(m) != 8 {
			continue
		}
		mult, _ := strconv.Atoi(m[7])
		usage, _ := strconv.ParseFloat(m[5], 64)
		meters = append(meters, ParsedMeter{
			MeterType:       strings.ToLower(m[1]),
			MeterNumber:     m[2],
			PreviousReading: m[3],
			CurrentReading:  m[4],
			Usage:           usage,
			Multiplier:      mult,
		})
	}
	return meters
}
