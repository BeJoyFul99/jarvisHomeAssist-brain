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
	reAccount = regexp.MustCompile(`(?i)Account Number:\s*(\S+)`)
	reAddress = regexp.MustCompile(`(?im)Service Address:\s*(.+?)(?:\s{2,}|$)`)
	// Dates: accept either ISO (synthetic fixtures) or "Month D, YYYY" (real PowerStream bills).
	reStatementISO  = regexp.MustCompile(`(?i)Statement Date:\s*(\d{4}-\d{2}-\d{2})`)
	reStatementLong = regexp.MustCompile(`(?i)Statement Date:\s*((?:January|February|March|April|May|June|July|August|September|October|November|December)\s+\d{1,2},\s*\d{4})`)
	reDueISO        = regexp.MustCompile(`(?i)Due Date:\s*(\d{4}-\d{2}-\d{2})`)
	reDueLong       = regexp.MustCompile(`(?i)Due Date:\s*((?:January|February|March|April|May|June|July|August|September|October|November|December)\s+\d{1,2},\s*\d{4})`)
	reBillType      = regexp.MustCompile(`(?i)Bill Type:\s*(\S+)`)
	// Total: prefer the printed "Bill at a Glance" value (real bills) and fall back
	// to TOTAL CURRENT CHARGES (fixtures).
	reTotalGlance = regexp.MustCompile(`(?is)Bill at a Glance\s*\$?\s*([\d.,]+)`)
	reTotalLong   = regexp.MustCompile(`(?im)^TOTAL CURRENT CHARGES\s+\$?([-\d.,]+)`)
	rePrevBal     = regexp.MustCompile(`(?im)PREVIOUS BALANCE\s+\$?([-\d.,]+)`)
	rePayment     = regexp.MustCompile(`(?im)^PAYMENT\s+[-/\d]+\s+-\$?([-\d.,]+)`)
	reBalFwd      = regexp.MustCompile(`(?im)BALANCE FORWARD\s+-?\$?([-\d.,]+)`)
	reLateFee     = regexp.MustCompile(`(?im)Late Fee\s+\$?([-\d.,]+)`)
	// Line item: description, optional "<n> kWh|m3", optional "@ $<rate>", sign, amount.
	reLineItem = regexp.MustCompile(`(?m)^\s*(.+?)\s+(\d+\s+(?:kWh|m3))?\s*(?:@\s*\$?([\d.]+))?\s+(-?)\$?([\d.,]+)\s*$`)
	// Two meter layouts: fixture (Prev:/Curr:/Usage:/Multiplier:) and real (NUM CURR/DATE PREV/DATE MULT).
	reMeterFixture = regexp.MustCompile(`(?im)^(Electric|Water|HVAC)\s+(\S+)\s+Prev:\s*(\S+)\s+Curr:\s*(\S+)\s+Usage:\s*([\d.]+)\s+(\w+)\s+Multiplier:\s*(\d+)`)
	reMeterReal    = regexp.MustCompile(
		`(?im)^(Electric|Water|HVAC)\s+Meter\s+(\S+)\s+(\S+)\s*/\s*([A-Z][a-z]+\s+\d{1,2},\s*\d{4})\s+(\S+)\s*/\s*([A-Z][a-z]+\s+\d{1,2},\s*\d{4})\s+(\d+)`,
	)
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
	p.StatementDate = firstParseableDate(text, reStatementISO, reStatementLong)
	p.DueDate = firstParseableDate(text, reDueISO, reDueLong)
	if m := reBillType.FindStringSubmatch(text); len(m) == 2 {
		p.BillType = strings.TrimSpace(m[1])
	}
	// Total: try the "Bill at a Glance" figure first (what real bills print),
	// then fall back to the synthetic "TOTAL CURRENT CHARGES" line.
	if m := reTotalGlance.FindStringSubmatch(text); len(m) == 2 {
		p.TotalAmount = parseAmount(m[1])
	}
	if p.TotalAmount == 0 {
		if m := reTotalLong.FindStringSubmatch(text); len(m) == 2 {
			p.TotalAmount = parseAmount(m[1])
		}
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

// firstParseableDate walks the supplied regexes in order and returns the first
// match that time.Parse can decode in either ISO ("2006-01-02") or long
// ("January 2, 2006") form. Returns zero time.Time when nothing matches.
func firstParseableDate(text string, patterns ...*regexp.Regexp) time.Time {
	layouts := []string{"2006-01-02", "January 2, 2006"}
	for _, re := range patterns {
		m := re.FindStringSubmatch(text)
		if len(m) != 2 {
			continue
		}
		raw := strings.TrimSpace(m[1])
		for _, layout := range layouts {
			if t, err := time.Parse(layout, raw); err == nil {
				return t
			}
		}
	}
	return time.Time{}
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

	// Real PowerStream layout: "Electric Meter NUM CURR / Mar 16, 2026  PREV / Feb 16, 2026  MULT"
	for _, m := range reMeterReal.FindAllStringSubmatch(text, -1) {
		if len(m) != 8 {
			continue
		}
		mult, _ := strconv.Atoi(m[7])
		currReading := m[3]
		currDate, _ := time.Parse("January 2, 2006", expandMonth(m[4]))
		prevReading := m[5]
		prevDate, _ := time.Parse("January 2, 2006", expandMonth(m[6]))
		usage := numericDelta(currReading, prevReading) * float64(max(mult, 1))
		meters = append(meters, ParsedMeter{
			MeterType:        strings.ToLower(m[1]),
			MeterNumber:      m[2],
			PreviousReading:  prevReading,
			PreviousReadDate: prevDate,
			CurrentReading:   currReading,
			CurrentReadDate:  currDate,
			Usage:            usage,
			Multiplier:       mult,
		})
	}

	// Synthetic fixture layout: "Electric E12345 Prev: X Curr: Y Usage: Z kWh Multiplier: N"
	for _, m := range reMeterFixture.FindAllStringSubmatch(text, -1) {
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

// expandMonth turns a 3-letter abbreviation ("Mar") into the full month name
// ("March") so time.Parse with "January 2, 2006" succeeds on both.
func expandMonth(s string) string {
	abbr := map[string]string{
		"Jan": "January", "Feb": "February", "Mar": "March", "Apr": "April",
		"May": "May", "Jun": "June", "Jul": "July", "Aug": "August",
		"Sep": "September", "Oct": "October", "Nov": "November", "Dec": "December",
	}
	parts := strings.SplitN(s, " ", 2)
	if len(parts) == 2 {
		if full, ok := abbr[parts[0]]; ok {
			return full + " " + parts[1]
		}
	}
	return s
}

func numericDelta(a, b string) float64 {
	strip := func(s string) float64 {
		cleaned := strings.TrimLeft(s, "0")
		if cleaned == "" {
			return 0
		}
		v, _ := strconv.ParseFloat(cleaned, 64)
		return v
	}
	return strip(a) - strip(b)
}
