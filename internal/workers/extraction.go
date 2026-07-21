package workers

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/bills"
	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/sse"
)

// ExtractionJob is the unit of work sent to the extraction worker.
type ExtractionJob struct {
	BillID uint
}

// ExtractorFunc abstracts bills.Extractor so tests can inject a stub.
type ExtractorFunc interface {
	Extract(ctx context.Context, data []byte) (bills.ExtractResult, error)
}

// RunExtractionWorker drains jobs from `in` until ctx is cancelled.
func RunExtractionWorker(
	ctx context.Context,
	in <-chan ExtractionJob,
	db *gorm.DB,
	store bills.BillStore,
	extractor ExtractorFunc,
	hub *sse.Hub,
	log *logger.Logger,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-in:
			if !ok {
				return
			}
			runOne(ctx, job, db, store, extractor, hub, log)
		}
	}
}

func runOne(
	ctx context.Context,
	job ExtractionJob,
	db *gorm.DB,
	store bills.BillStore,
	extractor ExtractorFunc,
	hub *sse.Hub,
	log *logger.Logger,
) {
	var bill models.UtilityBill
	if err := db.First(&bill, job.BillID).Error; err != nil {
		log.Error("bills", fmt.Sprintf("extraction: bill %d not found: %v", job.BillID, err))
		return
	}
	hub.Broadcast(sse.Event{Type: sse.EventBillExtractionStarted, Data: sse.BillExtractionEvent{BillID: bill.ID}})

	data, err := store.Get(bill.FilePath)
	if err != nil {
		markFailed(db, hub, &bill, fmt.Sprintf("store.Get: %v", err))
		return
	}

	// Surface pipeline stage notes to the UI when the extractor supports it.
	runner := extractor
	if pc, ok := extractor.(interface {
		WithProgress(func(string)) *bills.Extractor
	}); ok {
		runner = pc.WithProgress(func(note string) {
			hub.Broadcast(sse.Event{
				Type: sse.EventBillExtractionProgress,
				Data: sse.BillExtractionEvent{BillID: bill.ID, Note: note},
			})
		})
	}
	res, err := runner.Extract(ctx, data)
	if err != nil {
		markFailed(db, hub, &bill, err.Error())
		return
	}
	if res.Status == "failed" {
		markFailed(db, hub, &bill, res.Error)
		return
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		bill.ExtractionStatus = res.Status
		bill.ExtractionMethod = &res.Method
		bill.ExtractionConfidence = &res.Confidence
		bill.ExtractionError = nil
		if res.Model != "" {
			m := res.Model
			bill.ExtractionModel = &m
		} else {
			bill.ExtractionModel = nil
		}
		if !res.Parsed.StatementDate.IsZero() {
			bill.StatementDate = res.Parsed.StatementDate
		}
		if !res.Parsed.DueDate.IsZero() {
			bill.DueDate = res.Parsed.DueDate
		}
		if !res.Parsed.BillingPeriodStart.IsZero() {
			bill.BillingPeriodStart = res.Parsed.BillingPeriodStart
		}
		if !res.Parsed.BillingPeriodEnd.IsZero() {
			bill.BillingPeriodEnd = res.Parsed.BillingPeriodEnd
		}
		if res.Parsed.BillType != "" {
			bill.BillType = res.Parsed.BillType
		}
		bill.TotalAmount = res.Parsed.TotalAmount
		bill.PreviousBalance = res.Parsed.PreviousBalance
		bill.PaymentsReceived = res.Parsed.PaymentsReceived
		bill.BalanceForward = res.Parsed.BalanceForward
		bill.LateFees = res.Parsed.LateFees
		if res.Parsed.Currency != "" {
			bill.Currency = res.Parsed.Currency
		}
		if err := tx.Save(&bill).Error; err != nil {
			return err
		}
		if err := tx.Where("bill_id = ?", bill.ID).Delete(&models.UtilityBillLineItem{}).Error; err != nil {
			return err
		}
		if err := tx.Where("bill_id = ?", bill.ID).Delete(&models.UtilityBillMeter{}).Error; err != nil {
			return err
		}
		for _, li := range res.Parsed.LineItems {
			row := models.UtilityBillLineItem{
				BillID: bill.ID, UtilityType: li.UtilityType, Category: li.Category,
				Description: li.Description, UsageAmount: li.UsageAmount, UsageUnit: li.UsageUnit,
				Rate: li.Rate, Amount: li.Amount,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		for _, m := range res.Parsed.Meters {
			row := models.UtilityBillMeter{
				BillID: bill.ID, MeterType: m.MeterType, MeterNumber: m.MeterNumber,
				PreviousReading: m.PreviousReading, PreviousReadDate: m.PreviousReadDate,
				CurrentReading: m.CurrentReading, CurrentReadDate: m.CurrentReadDate,
				Usage: m.Usage, Multiplier: m.Multiplier,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		markFailed(db, hub, &bill, fmt.Sprintf("persist: %v", err))
		return
	}

	hub.Broadcast(sse.Event{Type: sse.EventBillExtractionCompleted, Data: sse.BillExtractionEvent{
		BillID: bill.ID, Status: res.Status, Method: res.Method,
		Confidence: res.Confidence, NeedsReview: res.Status == "needs_review",
	}})
	hub.Broadcast(sse.Event{Type: sse.EventBillImported, Data: sse.BillExtractionEvent{BillID: bill.ID}})

	if res.Status == "completed" || res.Status == "needs_review" {
		evaluateAndFire(db, hub, &bill)
	}
}

func evaluateAndFire(db *gorm.DB, hub *sse.Hub, bill *models.UtilityBill) {
	var budget models.EnergyBudget
	var proj bills.Projection

	// Only look up the monthly budget/projection when we have a real statement
	// date — otherwise we would query month=1/year=1 and GORM would log
	// "record not found" noise. Without a date, budget-related triggers are
	// silently skipped by EvaluateTriggers (BudgetAmount == 0 guard).
	if !bill.StatementDate.IsZero() {
		month := int(bill.StatementDate.Month())
		year := bill.StatementDate.Year()
		// Find() instead of First() so a missing row does not log as an error.
		db.Where("property_id = ? AND month = ? AND year = ?",
			bill.PropertyID, month, year,
		).Limit(1).Find(&budget)
		proj, _ = bills.ProjectCost(db, bill.PropertyID, month, year)
	}

	triggers := bills.EvaluateTriggers(*bill, budget, proj, time.Now().UTC())

	for _, tr := range triggers {
		inserted, _ := bills.TryInsertNotification(db, bills.NotificationKey{
			PropertyID: bill.PropertyID, BillID: &bill.ID,
			Trigger: tr.Name, PeriodKey: bills.PeriodKey(*bill, tr.Name),
		})
		if inserted {
			hub.Broadcast(sse.Event{
				Type: "bill:alert:" + tr.Name,
				Data: map[string]any{"bill_id": bill.ID, "reason": tr.Reason},
			})
		}
	}
}

func markFailed(db *gorm.DB, hub *sse.Hub, bill *models.UtilityBill, msg string) {
	bill.ExtractionStatus = "failed"
	bill.ExtractionError = &msg
	_ = db.Save(bill).Error
	hub.Broadcast(sse.Event{Type: sse.EventBillExtractionFailed, Data: sse.BillExtractionEvent{BillID: bill.ID, Error: msg}})
}
