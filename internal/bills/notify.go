package bills

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
)

// NotificationKey is the dedup tuple — matches UNIQUE(property_id, trigger, period_key).
type NotificationKey struct {
	PropertyID uint
	BillID     *uint
	Trigger    string
	PeriodKey  string // "YYYY-MM"
}

// TryInsertNotification returns (true, nil) on success, (false, nil) when UNIQUE
// blocks the insert, (false, err) for any other failure.
func TryInsertNotification(db *gorm.DB, key NotificationKey) (bool, error) {
	row := models.UtilityBillNotification{
		PropertyID: key.PropertyID, BillID: key.BillID,
		Trigger: key.Trigger, PeriodKey: key.PeriodKey,
	}
	err := db.Create(&row).Error
	if err == nil {
		return true, nil
	}
	if isUniqueViolation(err) {
		return false, nil
	}
	return false, err
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
