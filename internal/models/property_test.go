package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func TestProperty_CRUD(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}))

	p := models.Property{
		Name:          "Home",
		Address:       "123 Example Ave, Markham ON",
		MeterNumbers:  datatypes.JSON(`[{"type":"electric","number":"1234","multiplier":40}]`),
		AccountNumber: "987654321",
		Provider:      "powerstream",
		RateClass:     "Residential",
		IsActive:      true,
	}
	require.NoError(t, db.Create(&p).Error)
	require.NotZero(t, p.ID)

	var got models.Property
	require.NoError(t, db.First(&got, p.ID).Error)
	require.Equal(t, "Home", got.Name)
	require.Equal(t, "powerstream", got.Provider)
}
