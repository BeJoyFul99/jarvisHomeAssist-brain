package bills_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"jarvishomeassist-brain/internal/bills"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func TestBuildChatContext_SingleProperty_IncludesRecentBillsAndBudget(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}, &models.UtilityBillLineItem{}, &models.UtilityBillMeter{}, &models.EnergyBudget{}))
	prop := models.Property{Name: "Home", Address: "123 Example Ave", Provider: "powerstream", IsActive: true}
	require.NoError(t, db.Create(&prop).Error)

	for i := 0; i < 3; i++ {
		d := time.Date(2026, time.Month(4-i), 1, 0, 0, 0, 0, time.UTC)
		require.NoError(t, db.Create(&models.UtilityBill{
			PropertyID: prop.ID, UploadedBy: 1, FileHash: d.Format("2006-01"),
			Currency: "CAD", PaymentStatus: "paid",
			IngestionSource: "manual_upload", ExtractionStatus: "completed",
			StatementDate: d, DueDate: d.AddDate(0, 0, 21),
			TotalAmount: 100 + float64(10*i),
		}).Error)
	}
	pid := prop.ID
	require.NoError(t, db.Create(&models.EnergyBudget{
		Month: 4, Year: 2026, BudgetAmount: 150, AlertThresholdPct: 80, Currency: "CAD", PropertyID: &pid,
	}).Error)

	out, err := bills.BuildChatContext(db, prop.ID)
	require.NoError(t, err)
	require.Contains(t, out, "PROPERTY: Home")
	require.Contains(t, out, "123 Example Ave")
	require.Contains(t, out, "PowerStream")
	require.Contains(t, out, "BUDGET: $150")
	require.Contains(t, out, "LAST 3 BILLS:")
	require.True(t, strings.Contains(out, "$100") || strings.Contains(out, "$110") || strings.Contains(out, "$120"))
}

func TestBuildChatContext_NoProperty_ReturnsEmpty(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}))
	out, err := bills.BuildChatContext(db, 999)
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestBuildChatContext_TokenCap(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}))
	prop := models.Property{Name: "Home", Address: "1", Provider: "powerstream", IsActive: true}
	require.NoError(t, db.Create(&prop).Error)
	for i := 0; i < 50; i++ {
		d := time.Date(2022, time.Month(1), 1, 0, 0, 0, 0, time.UTC).AddDate(0, i, 0)
		require.NoError(t, db.Create(&models.UtilityBill{
			PropertyID: prop.ID, UploadedBy: 1, FileHash: d.Format("2006-01"),
			Currency: "CAD", PaymentStatus: "paid",
			IngestionSource: "manual_upload", ExtractionStatus: "completed",
			StatementDate: d, DueDate: d.AddDate(0, 0, 21), TotalAmount: 100,
		}).Error)
	}
	out, err := bills.BuildChatContext(db, prop.ID)
	require.NoError(t, err)
	require.LessOrEqual(t, len(out), 8000)
}
