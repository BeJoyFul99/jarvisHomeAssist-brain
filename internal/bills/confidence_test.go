package bills_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/bills"
)

func fullParsed() bills.ParsedBill {
	amount := 20.69
	rate := 0.10345
	unit := "kWh"
	usage := 200.0
	return bills.ParsedBill{
		AccountNumber:  "987654321",
		ServiceAddress: "123 EXAMPLE AVE",
		StatementDate:  time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		DueDate:        time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		BillType:       "REGULAR",
		TotalAmount:    20.69,
		Currency:       "CAD",
		LineItems: []bills.ParsedLineItem{
			{UtilityType: "electricity", Category: "energy", Description: "Tier 1",
				Amount: amount, Rate: &rate, UsageUnit: &unit, UsageAmount: &usage},
		},
		Meters: []bills.ParsedMeter{{MeterType: "electric", MeterNumber: "E1", Usage: 200, Multiplier: 40}},
	}
}

func TestScoreConfidence_FullBill_100(t *testing.T) {
	require.Equal(t, 100, bills.ScoreConfidence(fullParsed()))
}

func TestScoreConfidence_MissingTotal_LowersScore(t *testing.T) {
	p := fullParsed()
	p.TotalAmount = 0
	require.Less(t, bills.ScoreConfidence(p), 80)
}

func TestScoreConfidence_LineItemSumMismatch_Lowers(t *testing.T) {
	p := fullParsed()
	p.TotalAmount = 9999
	score := bills.ScoreConfidence(p)
	require.GreaterOrEqual(t, score, 80)
	require.Less(t, score, 100)
}

func TestScoreConfidence_NoFields_0(t *testing.T) {
	require.Equal(t, 0, bills.ScoreConfidence(bills.ParsedBill{}))
}
