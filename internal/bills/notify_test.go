package bills_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"jarvishomeassist-brain/internal/bills"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func TestTryInsertNotification_FirstTime(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBillNotification{}))
	prop := models.Property{Name: "Home", Address: "1", Provider: "powerstream"}
	require.NoError(t, db.Create(&prop).Error)

	inserted, err := bills.TryInsertNotification(db, bills.NotificationKey{
		PropertyID: prop.ID, Trigger: "budget_warning", PeriodKey: "2026-04",
	})
	require.NoError(t, err)
	require.True(t, inserted)
}

func TestTryInsertNotification_DuplicateReturnsFalse(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBillNotification{}))
	prop := models.Property{Name: "Home", Address: "1", Provider: "powerstream"}
	require.NoError(t, db.Create(&prop).Error)

	key := bills.NotificationKey{PropertyID: prop.ID, Trigger: "budget_warning", PeriodKey: "2026-04"}
	_, err := bills.TryInsertNotification(db, key)
	require.NoError(t, err)
	inserted, err := bills.TryInsertNotification(db, key)
	require.NoError(t, err)
	require.False(t, inserted)

	var count int64
	db.Model(&models.UtilityBillNotification{}).Count(&count)
	require.Equal(t, int64(1), count)
}
