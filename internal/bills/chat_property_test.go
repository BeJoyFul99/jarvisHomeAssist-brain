package bills_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"jarvishomeassist-brain/internal/bills"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func TestResolveProperty_PageURLWins(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}))
	require.NoError(t, db.Create(&models.Property{Name: "Home", Address: "1", Provider: "powerstream", IsActive: true}).Error)
	require.NoError(t, db.Create(&models.Property{Name: "Cottage", Address: "2", Provider: "powerstream", IsActive: true}).Error)

	resolved, err := bills.ResolveProperty(db, bills.ChatContextHints{PageURL: "/utilities/2/overview"})
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, uint(2), resolved.ID)
}

func TestResolveProperty_NamedInMessage(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}))
	require.NoError(t, db.Create(&models.Property{Name: "Home", Address: "1", Provider: "powerstream", IsActive: true}).Error)
	cot := models.Property{Name: "Cottage", Address: "123 Muskoka Rd", Provider: "powerstream", IsActive: true}
	require.NoError(t, db.Create(&cot).Error)

	res, err := bills.ResolveProperty(db, bills.ChatContextHints{Message: "what was my cottage electric bill last month"})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, cot.ID, res.ID)

	res2, err := bills.ResolveProperty(db, bills.ChatContextHints{Message: "show me 123 muskoka's meter reading"})
	require.NoError(t, err)
	require.NotNil(t, res2)
	require.Equal(t, cot.ID, res2.ID)
}

func TestResolveProperty_FallsBackToOnlyProperty(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}))
	only := models.Property{Name: "Home", Address: "1", Provider: "powerstream", IsActive: true}
	require.NoError(t, db.Create(&only).Error)

	res, err := bills.ResolveProperty(db, bills.ChatContextHints{Message: "how's my bill doing"})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, only.ID, res.ID)
}

func TestResolveProperty_MultiWithoutCue_ReturnsNil(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}))
	require.NoError(t, db.Create(&models.Property{Name: "Home", Address: "1", Provider: "powerstream", IsActive: true}).Error)
	require.NoError(t, db.Create(&models.Property{Name: "Cottage", Address: "2", Provider: "powerstream", IsActive: true}).Error)

	res, err := bills.ResolveProperty(db, bills.ChatContextHints{Message: "how are my bills"})
	require.NoError(t, err)
	require.Nil(t, res, "multi-property without a cue should return nil so caller can inject a summary")
}
