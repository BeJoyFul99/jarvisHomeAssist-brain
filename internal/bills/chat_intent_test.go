package bills_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"jarvishomeassist-brain/internal/bills"
)

func TestDetectUtilityIntent_PositiveCases(t *testing.T) {
	cases := []string{
		"What was my electricity bill last month?",
		"how much water did I use",
		"am I going to exceed my budget this month on electric",
		"when is my utility payment due",
		"hvac cost breakdown please",
		"show my last utility statement",
		"what's my meter reading",
	}
	for _, m := range cases {
		require.True(t, bills.DetectUtilityIntent(m), "expected utility intent for %q", m)
	}
}

func TestDetectUtilityIntent_NegativeCases(t *testing.T) {
	cases := []string{
		"what's the weather today",
		"remind me to call mom",
		"play some jazz",
		"",
		"Can you turn on the kitchen lights?",
	}
	for _, m := range cases {
		require.False(t, bills.DetectUtilityIntent(m), "expected no utility intent for %q", m)
	}
}

func TestDetectUtilityIntent_BudgetAlone_DoesNotTrigger(t *testing.T) {
	require.False(t, bills.DetectUtilityIntent("I need to budget my weekend"))
}
