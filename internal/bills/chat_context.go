package bills

import (
	"fmt"
	"strings"

	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
)

// BuildChatContext produces the system-prompt context block for a property.
// Caps at ~1,500 tokens (~6-7k chars). Spec §5.2.
func BuildChatContext(db *gorm.DB, propertyID uint) (string, error) {
	var prop models.Property
	if err := db.First(&prop, propertyID).Error; err != nil {
		return "", nil // silent skip — caller falls through without context
	}

	var recent []models.UtilityBill
	db.Where("property_id = ? AND extraction_status = ?", propertyID, "completed").
		Order("statement_date desc").Limit(3).Find(&recent)

	var currentBudget models.EnergyBudget
	if len(recent) > 0 {
		m := int(recent[0].StatementDate.Month())
		y := recent[0].StatementDate.Year()
		db.Where("property_id = ? AND month = ? AND year = ?", propertyID, m, y).First(&currentBudget)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "USER UTILITY DATA:\n")
	fmt.Fprintf(&sb, "PROPERTY: %s — %s\n", prop.Name, prop.Address)
	fmt.Fprintf(&sb, "PROVIDER: PowerStream Energy Services\n")
	if currentBudget.BudgetAmount > 0 {
		fmt.Fprintf(&sb, "BUDGET: $%.2f/month (alert threshold %d%%)\n",
			currentBudget.BudgetAmount, currentBudget.AlertThresholdPct)
	}

	if len(recent) > 0 {
		sb.WriteString("\nLAST 3 BILLS:\n")
		for _, b := range recent {
			fmt.Fprintf(&sb, "- %s: $%.2f total, due %s, status %s\n",
				b.StatementDate.Format("Jan 2006"), b.TotalAmount,
				b.DueDate.Format("2006-01-02"), b.PaymentStatus)
		}
		var items []models.UtilityBillLineItem
		db.Where("bill_id = ?", recent[0].ID).Find(&items)
		if len(items) > 0 {
			sb.WriteString("\nCURRENT BILL BREAKDOWN:\n")
			totals := map[string]float64{}
			for _, li := range items {
				totals[li.UtilityType] += li.Amount
			}
			for u, total := range totals {
				fmt.Fprintf(&sb, "- %s: $%.2f\n", u, total)
			}
			if recent[0].LateFees > 0 {
				fmt.Fprintf(&sb, "- late fees: $%.2f\n", recent[0].LateFees)
			}
			fmt.Fprintf(&sb, "- total: $%.2f\n", recent[0].TotalAmount)
		}
	}

	sb.WriteString("\nUse this data to answer the user's question with specific numbers. Do not invent data beyond what is listed.\n")

	out := sb.String()
	const maxChars = 7000
	if len(out) > maxChars {
		out = out[:maxChars] + "\n... (context truncated)"
	}
	return out, nil
}
