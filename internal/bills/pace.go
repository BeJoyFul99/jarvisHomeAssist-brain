package bills

import (
	"time"

	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
)

type Baseline string

const (
	BaselineYoY       Baseline = "yoy"
	BaselineTrailing3 Baseline = "trailing_3mo"
	BaselineUnknown   Baseline = "unknown"
)

// Projection is the output of ProjectCost. Confidence is "high" for YoY,
// "low" for trailing-3-month fallback. Spec §4.2.
type Projection struct {
	Baseline      Baseline `json:"baseline"`
	Projected     float64  `json:"projected"`
	Confidence    string   `json:"confidence"`
	ReferenceNote string   `json:"reference_note"`
}

// ProjectCost projects `month`/`year` for `propertyID` using YoY-first,
// trailing-3-month fallback (spec §4.2).
func ProjectCost(db *gorm.DB, propertyID uint, month, year int) (Projection, error) {
	prevYear := year - 1
	yoy := sumBillsForMonth(db, propertyID, month, prevYear)
	if yoy > 0 {
		scale := ytdScale(db, propertyID, month-1, year, prevYear)
		return Projection{
			Baseline: BaselineYoY, Projected: yoy * scale, Confidence: "high",
			ReferenceNote: "YoY same-month baseline, scaled by YTD ratio",
		}, nil
	}
	avg, count := trailingAverage(db, propertyID, month, year, 3)
	if count > 0 {
		return Projection{
			Baseline: BaselineTrailing3, Projected: avg, Confidence: "low",
			ReferenceNote: "trailing 3-month average (YoY unavailable)",
		}, nil
	}
	return Projection{Baseline: BaselineUnknown}, nil
}

// sumBillsForMonth sums all bills for a given month/year.
// Portable date range — works on both Postgres and SQLite (no EXTRACT()).
func sumBillsForMonth(db *gorm.DB, propID uint, month, year int) float64 {
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	var out []models.UtilityBill
	db.Where("property_id = ? AND statement_date >= ? AND statement_date < ?", propID, start, end).
		Find(&out)
	var total float64
	for _, b := range out {
		total += b.TotalAmount
	}
	return total
}

// ytdScale computes year-to-date scale: average of non-zero months this year
// divided by average of non-zero months in previous year, through the given month.
func ytdScale(db *gorm.DB, propID uint, throughMonth, year, prevYear int) float64 {
	if throughMonth < 1 {
		return 1.0
	}
	thisSum, thisCount := 0.0, 0
	prevSum, prevCount := 0.0, 0
	for m := 1; m <= throughMonth; m++ {
		if v := sumBillsForMonth(db, propID, m, year); v > 0 {
			thisSum += v
			thisCount++
		}
		if v := sumBillsForMonth(db, propID, m, prevYear); v > 0 {
			prevSum += v
			prevCount++
		}
	}
	if prevCount == 0 || thisCount == 0 {
		return 1.0
	}
	prevAvg := prevSum / float64(prevCount)
	thisAvg := thisSum / float64(thisCount)
	return thisAvg / prevAvg
}

// trailingAverage computes the average and count of bills in the trailing
// windowMonths ending at the start of the given month.
func trailingAverage(db *gorm.DB, propID uint, month, year, windowMonths int) (float64, int) {
	end := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, -windowMonths, 0)
	var out []models.UtilityBill
	db.Where("property_id = ? AND statement_date >= ? AND statement_date < ?", propID, start, end).
		Find(&out)
	if len(out) == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, b := range out {
		sum += b.TotalAmount
	}
	return sum / float64(len(out)), len(out)
}
