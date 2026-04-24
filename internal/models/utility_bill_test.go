package models_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func TestUtilityBill_CRUDAndDefaults(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}))

	prop := models.Property{Name: "Home", Address: "123 A St", Provider: "powerstream"}
	require.NoError(t, db.Create(&prop).Error)

	bill := models.UtilityBill{
		PropertyID:         prop.ID,
		UploadedBy:         1,
		FileHash:           "abc123",
		StatementDate:      time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		DueDate:            time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		BillingPeriodStart: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		BillingPeriodEnd:   time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		BillType:           "REGULAR",
		TotalAmount:        234.56,
		Currency:           "CAD",
		PaymentStatus:      "unpaid",
		IngestionSource:    "manual_upload",
		ExtractionStatus:   "processing",
		RawExtractedData:   datatypes.JSON(`{}`),
	}
	require.NoError(t, db.Create(&bill).Error)
	require.NotZero(t, bill.ID)

	var got models.UtilityBill
	require.NoError(t, db.First(&got, bill.ID).Error)
	require.Equal(t, "unpaid", got.PaymentStatus)
	require.Equal(t, "processing", got.ExtractionStatus)
}

func TestUtilityBill_UniqueHashPerProperty(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}))

	prop := models.Property{Name: "Home", Address: "1", Provider: "powerstream"}
	require.NoError(t, db.Create(&prop).Error)

	b1 := models.UtilityBill{PropertyID: prop.ID, UploadedBy: 1, FileHash: "dup", Currency: "CAD", PaymentStatus: "unpaid", IngestionSource: "manual_upload", ExtractionStatus: "processing"}
	require.NoError(t, db.Create(&b1).Error)

	b2 := models.UtilityBill{PropertyID: prop.ID, UploadedBy: 1, FileHash: "dup", Currency: "CAD", PaymentStatus: "unpaid", IngestionSource: "manual_upload", ExtractionStatus: "processing"}
	require.Error(t, db.Create(&b2).Error, "duplicate (property_id,file_hash) must violate UNIQUE")
}
