package handlers_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"jarvishomeassist-brain/internal/handlers"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func TestChatHandler_UtilityContextPrefix(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}, &models.UtilityBillLineItem{}, &models.EnergyBudget{}))
	require.NoError(t, db.Create(&models.Property{Name: "Home", Address: "1", Provider: "powerstream", IsActive: true}).Error)

	h := &handlers.ChatHandler{DB: db}
	// Non-utility message → empty
	require.Empty(t, h.BuildUtilityContextIfRelevantForTest("play music", ""))
	// Utility message → non-empty context
	out := h.BuildUtilityContextIfRelevantForTest("what was my electricity bill", "")
	require.Contains(t, out, "USER UTILITY DATA:")
}
