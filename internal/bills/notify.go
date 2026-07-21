package bills

import (
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"jarvishomeassist-brain/internal/models"
)

// NotificationKey is the dedup tuple — matches UNIQUE(property_id, trigger, period_key).
type NotificationKey struct {
	PropertyID uint
	BillID     *uint
	Trigger    string
	PeriodKey  string // "YYYY-MM" for month-scoped triggers, "bill-<id>" for per-bill.
}

// TryInsertNotification returns (true, nil) when a new row was actually written,
// (false, nil) when the dedup tuple already exists, (false, err) on any other
// failure. Uses ON CONFLICT DO NOTHING so the driver does not log a UNIQUE
// violation at ERROR level when re-extracting an already-notified bill.
func TryInsertNotification(db *gorm.DB, key NotificationKey) (bool, error) {
	row := models.UtilityBillNotification{
		PropertyID: key.PropertyID, BillID: key.BillID,
		Trigger: key.Trigger, PeriodKey: key.PeriodKey,
	}
	res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		// Fallback: some driver combos may still surface a UNIQUE violation
		// even with OnConflict — treat those as silent skips.
		if isUniqueViolation(res.Error) {
			return false, nil
		}
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint") {
		return true
	}
	if strings.Contains(msg, "unique constraint failed") {
		return true
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	return false
}
