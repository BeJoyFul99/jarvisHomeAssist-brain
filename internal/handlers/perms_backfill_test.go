package handlers_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"jarvishomeassist-brain/internal/handlers"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func TestBackfillEmptyResourcePerms(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Setting{}))

	mkUser := func(email string, role models.Role, perms datatypes.JSON) models.User {
		u := models.User{Email: email, DisplayName: email, Role: role, ResourcePerms: perms}
		require.NoError(t, u.SetPassword("test-password"))
		require.NoError(t, db.Create(&u).Error)
		return u
	}

	legacyFamily := mkUser("family-empty@test.local", models.RoleFamilyMember, datatypes.JSON("[]"))
	legacyGuest := mkUser("guest-empty@test.local", models.RoleGuest, nil)
	customFamily := mkUser("family-custom@test.local", models.RoleFamilyMember, datatypes.JSON(`["media:view"]`))

	handlers.BackfillEmptyResourcePerms(db, nil)

	fetchPerms := func(id uint) []string {
		var got models.User
		require.NoError(t, db.First(&got, id).Error)
		return got.GetResourcePerms()
	}

	// Legacy family user with '[]' perms gets role defaults
	require.ElementsMatch(t, models.DefaultPermsForRole(models.RoleFamilyMember), fetchPerms(legacyFamily.ID))

	// Legacy guest with NULL perms gets guest defaults
	require.ElementsMatch(t, models.DefaultPermsForRole(models.RoleGuest), fetchPerms(legacyGuest.ID))

	// User with explicit custom perms is untouched
	require.ElementsMatch(t, []string{"media:view"}, fetchPerms(customFamily.ID))

	// The one-time flag is recorded
	var flag models.Setting
	require.NoError(t, db.Where("key = ?", handlers.PermsBackfillKey).First(&flag).Error)

	// Second run must be a no-op: deliberately emptied perms stay empty
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", legacyFamily.ID).
		Update("resource_perms", datatypes.JSON("[]")).Error)
	handlers.BackfillEmptyResourcePerms(db, nil)
	require.Empty(t, fetchPerms(legacyFamily.ID))
}
