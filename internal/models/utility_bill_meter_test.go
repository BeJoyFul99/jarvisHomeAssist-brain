package models_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func TestUtilityBillMeter_CRUD(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}, &models.UtilityBillMeter{}))

	prop := models.Property{Name: "Home", Address: "1", Provider: "powerstream"}
	require.NoError(t, db.Create(&prop).Error)
	bill := models.UtilityBill{PropertyID: prop.ID, UploadedBy: 1, FileHash: "h", Currency: "CAD", PaymentStatus: "unpaid", IngestionSource: "manual_upload", ExtractionStatus: "processing"}
	require.NoError(t, db.Create(&bill).Error)

	m := models.UtilityBillMeter{
		BillID:           bill.ID,
		MeterType:        "electric",
		MeterNumber:      "E12345",
		PreviousReading:  "05000",
		PreviousReadDate: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		CurrentReading:   "05250",
		CurrentReadDate:  time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		Usage:            250,
		Multiplier:       40,
	}
	require.NoError(t, db.Create(&m).Error)
	require.NotZero(t, m.ID)
}
