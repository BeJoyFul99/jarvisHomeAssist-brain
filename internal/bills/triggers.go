package bills

import (
	"fmt"
	"time"

	"jarvishomeassist-brain/internal/models"
)

// Trigger is an alert the evaluator decided should fire (spec §4.3 catalog).
type Trigger struct {
	Name   string
	Reason string
}

// EvaluateTriggers runs the §4.3 rules against a newly imported bill.
// `now` is injected for tests. Zero-value budget skips budget-related triggers.
func EvaluateTriggers(bill models.UtilityBill, budget models.EnergyBudget, proj Projection, now time.Time) []Trigger {
	var out []Trigger
	out = append(out, Trigger{Name: "bill_imported", Reason: "new bill available"})

	if budget.BudgetAmount > 0 {
		threshold := float64(budget.AlertThresholdPct) / 100.0 * budget.BudgetAmount
		if bill.TotalAmount > budget.BudgetAmount {
			out = append(out, Trigger{Name: "budget_exceeded", Reason: "bill exceeds monthly budget"})
		} else if proj.Projected > threshold {
			out = append(out, Trigger{Name: "budget_warning", Reason: "projected cost crosses alert threshold"})
		}
	}

	if bill.LateFees > 0 {
		out = append(out, Trigger{Name: "late_fee", Reason: "bill contains a late fee"})
	}

	if bill.PaymentStatus != "paid" && !bill.DueDate.IsZero() {
		days := int(bill.DueDate.Sub(now).Hours() / 24)
		if days >= 0 && days <= 2 {
			out = append(out, Trigger{Name: "payment_reminder", Reason: "bill due soon"})
		}
		if days < 0 {
			out = append(out, Trigger{Name: "payment_overdue", Reason: "due date passed"})
		}
	}
	return out
}

// PeriodKey returns the dedup key for (bill, trigger). Month-scoped triggers
// dedup once per property-month (so a second bill in the same month does not
// re-fire a budget warning). Bill-scoped triggers dedup per bill so every
// imported bill gets its own notification regardless of statement date.
func PeriodKey(bill models.UtilityBill, trigger string) string {
	switch trigger {
	case "budget_warning", "budget_exceeded":
		if !bill.StatementDate.IsZero() {
			return bill.StatementDate.Format("2006-01")
		}
		// Fall back to bill-scope when statement date is missing so we do not
		// collapse unrelated bills onto a single empty key.
	}
	return fmt.Sprintf("bill-%d", bill.ID)
}
