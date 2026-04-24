package workers_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/bills"
	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/sse"
	"jarvishomeassist-brain/internal/testutil"
	"jarvishomeassist-brain/internal/workers"
)

type stubExtractor struct{ result bills.ExtractResult }

func (s *stubExtractor) Extract(ctx context.Context, data []byte) (bills.ExtractResult, error) {
	return s.result, nil
}

type stubStore struct {
	data map[string][]byte
	mu   sync.Mutex
}

func (s *stubStore) Put(uint, string, string, []byte) (string, error) { return "bills/x.pdf", nil }
func (s *stubStore) Get(p string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[p], nil
}
func (s *stubStore) Delete(string) error { return nil }

func newLogger(t *testing.T) *logger.Logger {
	t.Helper()
	l, err := logger.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })
	return l
}

func seedBill(t *testing.T, db *gorm.DB) models.UtilityBill {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}, &models.UtilityBillLineItem{}, &models.UtilityBillMeter{}))
	prop := models.Property{Name: "Home", Address: "1", Provider: "powerstream"}
	require.NoError(t, db.Create(&prop).Error)
	bill := models.UtilityBill{
		PropertyID: prop.ID, UploadedBy: 1, FileHash: "h1", FilePath: "bills/x.pdf",
		Currency: "CAD", PaymentStatus: "unpaid",
		IngestionSource: "manual_upload", ExtractionStatus: "processing",
	}
	require.NoError(t, db.Create(&bill).Error)
	return bill
}

func TestExtractionWorker_WritesResultAndBroadcasts(t *testing.T) {
	db := testutil.NewTestDB(t)
	// In-memory SQLite with cache=private requires a single connection so all
	// goroutines (test goroutine + worker goroutine) share the same in-memory DB.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	log := newLogger(t)
	hub := sse.NewHub(log)
	bill := seedBill(t, db)

	store := &stubStore{data: map[string][]byte{"bills/x.pdf": []byte("%PDF-1.4\n...")}}
	extract := &stubExtractor{result: bills.ExtractResult{
		Status: "completed", Method: "structured", Confidence: 92,
		Parsed: bills.ParsedBill{
			AccountNumber: "987654321", BillType: "REGULAR", TotalAmount: 92.78, Currency: "CAD",
			LineItems: []bills.ParsedLineItem{{UtilityType: "electricity", Category: "energy", Description: "Tier 1", Amount: 92.78}},
			Meters:    []bills.ParsedMeter{{MeterType: "electric", MeterNumber: "E1", Usage: 200, Multiplier: 40}},
		},
	}}

	jobs := make(chan workers.ExtractionJob, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go workers.RunExtractionWorker(ctx, jobs, db, store, extract, hub, log)

	jobs <- workers.ExtractionJob{BillID: bill.ID}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var got models.UtilityBill
		require.NoError(t, db.First(&got, bill.ID).Error)
		if got.ExtractionStatus == "completed" {
			require.NotNil(t, got.ExtractionMethod)
			require.Equal(t, "structured", *got.ExtractionMethod)
			require.NotNil(t, got.ExtractionConfidence)
			require.Equal(t, 92, *got.ExtractionConfidence)

			var items []models.UtilityBillLineItem
			require.NoError(t, db.Where("bill_id = ?", bill.ID).Find(&items).Error)
			require.Len(t, items, 1)

			var meters []models.UtilityBillMeter
			require.NoError(t, db.Where("bill_id = ?", bill.ID).Find(&meters).Error)
			require.Len(t, meters, 1)
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("worker did not finish in time")
}
