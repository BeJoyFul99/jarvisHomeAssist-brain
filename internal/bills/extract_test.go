package bills_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/bills"
)

type fakeVision struct {
	result bills.ParsedBill
	err    error
}

func (f *fakeVision) Extract(ctx context.Context, pages [][]byte) (bills.ParsedBill, error) {
	return f.result, f.err
}

func goodVisionBill() bills.ParsedBill {
	return bills.ParsedBill{
		AccountNumber: "987654321", BillType: "REGULAR",
		StatementDate: mustDate("2026-04-01"), DueDate: mustDate("2026-04-21"),
		TotalAmount: 92.78, Currency: "CAD",
		LineItems: []bills.ParsedLineItem{{UtilityType: "electricity", Category: "energy", Description: "Tier 1", Amount: 20.69}},
		Meters:    []bills.ParsedMeter{{MeterType: "electric", MeterNumber: "E1", Usage: 200, Multiplier: 40}},
	}
}

func TestExtract_StructuredHighConfidence_Completes(t *testing.T) {
	data, err := readTestdata("sample-bill.txt")
	require.NoError(t, err)

	orch := &bills.Extractor{
		ExtractText: func(_ []byte) (string, error) { return string(data), nil },
		Rasterize:   func(_ []byte) ([][]byte, error) { return nil, nil },
		Vision:      &fakeVision{},
	}
	res, err := orch.Extract(context.Background(), []byte("%PDF-1.4\n..."))
	require.NoError(t, err)
	require.Equal(t, "completed", res.Status)
	require.Equal(t, "structured", res.Method)
	require.GreaterOrEqual(t, res.Confidence, 70)
}

func TestExtract_LowConfidence_FallsBackToVision(t *testing.T) {
	orch := &bills.Extractor{
		ExtractText: func(_ []byte) (string, error) { return "garbage no fields", nil },
		Rasterize:   func(_ []byte) ([][]byte, error) { return [][]byte{{0x89, 'P', 'N', 'G'}}, nil },
		Vision:      &fakeVision{result: goodVisionBill()},
	}
	res, err := orch.Extract(context.Background(), []byte("%PDF-1.4\n..."))
	require.NoError(t, err)
	require.Equal(t, "completed", res.Status)
	require.Equal(t, "vision_fallback", res.Method)
}

func TestExtract_VisionFails_ReturnsNeedsReview(t *testing.T) {
	orch := &bills.Extractor{
		ExtractText: func(_ []byte) (string, error) { return "garbage no fields", nil },
		Rasterize:   func(_ []byte) ([][]byte, error) { return [][]byte{{0x89}}, nil },
		Vision:      &fakeVision{err: errors.New("boom")},
	}
	res, err := orch.Extract(context.Background(), []byte("%PDF-1.4\n..."))
	require.NoError(t, err)
	require.Equal(t, "needs_review", res.Status)
}

func TestExtract_ValidatorRejects_ReturnsFailed(t *testing.T) {
	orch := &bills.Extractor{
		ExtractText: func(_ []byte) (string, error) { panic("unreached") },
		Rasterize:   func(_ []byte) ([][]byte, error) { panic("unreached") },
		Vision:      &fakeVision{},
	}
	res, err := orch.Extract(context.Background(), []byte("not a pdf"))
	require.NoError(t, err)
	require.Equal(t, "failed", res.Status)
	require.NotEmpty(t, res.Error)
}
