package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func TestUtilityBillNotification_DedupByTriggerAndMonth(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBillNotification{}))
	prop := models.Property{Name: "Home", Address: "1", Provider: "powerstream"}
	require.NoError(t, db.Create(&prop).Error)

	n1 := models.UtilityBillNotification{PropertyID: prop.ID, Trigger: "budget_warning", PeriodKey: "2026-04"}
	require.NoError(t, db.Create(&n1).Error)

	n2 := models.UtilityBillNotification{PropertyID: prop.ID, Trigger: "budget_warning", PeriodKey: "2026-04"}
	require.Error(t, db.Create(&n2).Error, "same trigger in same period must violate UNIQUE")

	n3 := models.UtilityBillNotification{PropertyID: prop.ID, Trigger: "budget_warning", PeriodKey: "2026-05"}
	require.NoError(t, db.Create(&n3).Error)

	n4 := models.UtilityBillNotification{PropertyID: prop.ID, Trigger: "late_fee", PeriodKey: "2026-04"}
	require.NoError(t, db.Create(&n4).Error)
}
