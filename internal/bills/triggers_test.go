package bills_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"jarvishomeassist-brain/internal/bills"
	"jarvishomeassist-brain/internal/models"
)

func TestEvaluateTriggers_BudgetExceededAndImportedAndReminder(t *testing.T) {
	bill := models.UtilityBill{
		PropertyID: 1, TotalAmount: 200,
		StatementDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		DueDate:       time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		PaymentStatus: "unpaid",
	}
	budget := models.EnergyBudget{Month: 4, Year: 2026, BudgetAmount: 150, AlertThresholdPct: 80}
	proj := bills.Projection{Baseline: bills.BaselineYoY, Projected: 200}
	triggers := bills.EvaluateTriggers(bill, budget, proj, time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC))
	names := triggerNames(triggers)
	require.Contains(t, names, "budget_exceeded")
	require.Contains(t, names, "bill_imported")
	require.Contains(t, names, "payment_reminder")
}

func TestEvaluateTriggers_LateFeeFires(t *testing.T) {
	bill := models.UtilityBill{
		PropertyID: 1, TotalAmount: 100, LateFees: 5.00,
		StatementDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		DueDate:       time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		PaymentStatus: "unpaid",
	}
	budget := models.EnergyBudget{Month: 4, Year: 2026, BudgetAmount: 500, AlertThresholdPct: 80}
	triggers := bills.EvaluateTriggers(bill, budget, bills.Projection{Projected: 100}, time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC))
	require.Contains(t, triggerNames(triggers), "late_fee")
}

func TestEvaluateTriggers_OverdueAfterDueDate(t *testing.T) {
	bill := models.UtilityBill{
		PropertyID: 1, TotalAmount: 100,
		StatementDate: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		DueDate:       time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC),
		PaymentStatus: "unpaid",
	}
	budget := models.EnergyBudget{Month: 3, Year: 2026, BudgetAmount: 500, AlertThresholdPct: 80}
	triggers := bills.EvaluateTriggers(bill, budget, bills.Projection{Projected: 100}, time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC))
	require.Contains(t, triggerNames(triggers), "payment_overdue")
}

func triggerNames(ts []bills.Trigger) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}
