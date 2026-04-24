package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/testutil"
)

func TestNewTestDB_OpensAndPings(t *testing.T) {
	db := testutil.NewTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
}
