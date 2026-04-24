package bills_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/bills"
)

func loadFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sample-bill.txt"))
	require.NoError(t, err)
	return string(data)
}

func TestParsePowerStream_AccountAndDates(t *testing.T) {
	p, err := bills.ParsePowerStream(loadFixture(t))
	require.NoError(t, err)
	require.Equal(t, "987654321", p.AccountNumber)
	require.Equal(t, "123 EXAMPLE AVE, MARKHAM ON", p.ServiceAddress)
	require.Equal(t, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), p.StatementDate)
	require.Equal(t, time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC), p.DueDate)
	require.Equal(t, "REGULAR", p.BillType)
}

func TestParsePowerStream_Totals(t *testing.T) {
	p, err := bills.ParsePowerStream(loadFixture(t))
	require.NoError(t, err)
	require.InDelta(t, 92.78, p.TotalAmount, 0.01)
	require.InDelta(t, 234.56, p.PreviousBalance, 0.01)
	require.InDelta(t, -234.56, p.PaymentsReceived, 0.01)
	require.InDelta(t, 0.00, p.BalanceForward, 0.01)
	require.InDelta(t, 5.00, p.LateFees, 0.01)
}

func TestParsePowerStream_LineItemsByUtility(t *testing.T) {
	p, err := bills.ParsePowerStream(loadFixture(t))
	require.NoError(t, err)
	require.Len(t, p.LineItemsByType("electricity"), 5)
	require.Len(t, p.LineItemsByType("water"), 2)
	require.Len(t, p.LineItemsByType("hvac"), 1)
	require.Len(t, p.LineItemsByType("other"), 1)
	var rebate bills.ParsedLineItem
	for _, li := range p.LineItemsByType("electricity") {
		if li.Category == "rebate" {
			rebate = li
		}
	}
	require.InDelta(t, -2.70, rebate.Amount, 0.01)
}

func TestParsePowerStream_Meters(t *testing.T) {
	p, err := bills.ParsePowerStream(loadFixture(t))
	require.NoError(t, err)
	require.Len(t, p.Meters, 3)
	require.Equal(t, "electric", p.Meters[0].MeterType)
	require.Equal(t, "E12345", p.Meters[0].MeterNumber)
	require.Equal(t, 40, p.Meters[0].Multiplier)
	require.InDelta(t, 250.0, p.Meters[0].Usage, 0.01)
}
