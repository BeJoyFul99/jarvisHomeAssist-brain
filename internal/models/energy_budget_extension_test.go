package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func TestEnergyBudget_HasPropertyIDAndAlertThreshold(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.EnergyBudget{}))

	prop := models.Property{Name: "Home", Address: "1", Provider: "powerstream"}
	require.NoError(t, db.Create(&prop).Error)

	pid := prop.ID
	b := models.EnergyBudget{Month: 4, Year: 2026, BudgetAmount: 300, Currency: "CAD", PropertyID: &pid, AlertThresholdPct: 85}
	require.NoError(t, db.Create(&b).Error)

	// NULL property_id — backward-compat path
	bg := models.EnergyBudget{Month: 5, Year: 2026, BudgetAmount: 300, Currency: "CAD"}
	require.NoError(t, db.Create(&bg).Error)
	require.Nil(t, bg.PropertyID)
	require.Equal(t, 80, bg.AlertThresholdPct, "default should be 80")
}
