package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func TestUtilityBillLineItem_CRUD(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}, &models.UtilityBillLineItem{}))

	prop := models.Property{Name: "Home", Address: "1", Provider: "powerstream"}
	require.NoError(t, db.Create(&prop).Error)
	bill := models.UtilityBill{PropertyID: prop.ID, UploadedBy: 1, FileHash: "h1", Currency: "CAD", PaymentStatus: "unpaid", IngestionSource: "manual_upload", ExtractionStatus: "processing"}
	require.NoError(t, db.Create(&bill).Error)

	usage := 250.0
	rate := 0.103
	unit := "kWh"
	li := models.UtilityBillLineItem{
		BillID:      bill.ID,
		UtilityType: "electricity",
		Category:    "energy",
		Description: "Tier 1 usage",
		UsageAmount: &usage,
		UsageUnit:   &unit,
		Rate:        &rate,
		Amount:      25.75,
	}
	require.NoError(t, db.Create(&li).Error)
	require.NotZero(t, li.ID)
}
