package bills

import "math"

// ScoreConfidence maps ParsedBill completeness + internal consistency to 0-100.
// Buckets (spec §3.2):
//   100    — all required fields + line-items sum matches total (±$0.50)
//   80-99  — all required fields present; sum mismatch
//   50-79  — some required fields missing
//   0-49   — severe gaps (caller routes to vision fallback)
func ScoreConfidence(p ParsedBill) int {
	required := []bool{
		p.AccountNumber != "",
		!p.StatementDate.IsZero(),
		!p.DueDate.IsZero(),
		p.BillType != "",
		p.TotalAmount > 0,
		len(p.LineItems) > 0,
		len(p.Meters) > 0,
	}
	present := 0
	for _, ok := range required {
		if ok {
			present++
		}
	}
	if present == 0 {
		return 0
	}
	base := int(math.Round(float64(present) / float64(len(required)) * 100))
	if present < len(required) {
		if base > 79 {
			base = 79
		}
		return base
	}

	// All required fields present — check line-item sum vs total.
	sum := 0.0
	for _, li := range p.LineItems {
		sum += li.Amount
	}
	if math.Abs(sum-p.TotalAmount) <= 0.50 {
		return 100
	}
	penalty := int(math.Min(20, math.Ceil(math.Abs(sum-p.TotalAmount))))
	score := 100 - penalty
	if score < 80 {
		score = 80
	}
	return score
}
