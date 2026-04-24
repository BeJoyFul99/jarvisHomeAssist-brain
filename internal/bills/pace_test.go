package bills_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/bills"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func seedBillPace(t *testing.T, db *gorm.DB, propID uint, statement time.Time, total float64) {
	t.Helper()
	b := models.UtilityBill{
		PropertyID: propID, UploadedBy: 1,
		FileHash: "h-" + statement.Format("2006-01"),
		Currency: "CAD", PaymentStatus: "paid",
		IngestionSource: "manual_upload", ExtractionStatus: "completed",
		StatementDate:      statement,
		DueDate:            statement.AddDate(0, 0, 21),
		BillingPeriodStart: statement.AddDate(0, -1, 0),
		BillingPeriodEnd:   statement.AddDate(0, 0, -1),
		TotalAmount:        total,
	}
	require.NoError(t, db.Create(&b).Error)
}

func TestProjectCost_UsesYoYWhenAvailable(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}))

	seedBillPace(t, db, 1, time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC), 180)
	seedBillPace(t, db, 1, time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), 220)
	seedBillPace(t, db, 1, time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC), 250)
	seedBillPace(t, db, 1, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), 240)

	proj, err := bills.ProjectCost(db, 1, 4, 2026)
	require.NoError(t, err)
	require.Equal(t, bills.BaselineYoY, proj.Baseline)
	require.InDelta(t, 184, proj.Projected, 10)
}

func TestProjectCost_FallsBackToTrailing3Months(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}))

	seedBillPace(t, db, 1, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), 200)
	seedBillPace(t, db, 1, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 180)
	seedBillPace(t, db, 1, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 220)

	proj, err := bills.ProjectCost(db, 1, 4, 2026)
	require.NoError(t, err)
	require.Equal(t, bills.BaselineTrailing3, proj.Baseline)
	require.InDelta(t, 200, proj.Projected, 1)
}

func TestProjectCost_NoData_ReturnsUnknown(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}))
	proj, err := bills.ProjectCost(db, 1, 4, 2026)
	require.NoError(t, err)
	require.Equal(t, bills.BaselineUnknown, proj.Baseline)
	require.Zero(t, proj.Projected)
}
