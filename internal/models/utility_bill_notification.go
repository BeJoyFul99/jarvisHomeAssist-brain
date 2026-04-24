package models

import "time"

// UtilityBillNotification — dedup ledger. UNIQUE(property_id, trigger, period_key). See spec §2.5, §4.3.
type UtilityBillNotification struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	PropertyID uint      `gorm:"not null;uniqueIndex:ux_util_notif" json:"property_id"`
	BillID     *uint     `gorm:"index" json:"bill_id"`
	Trigger    string    `gorm:"size:32;not null;uniqueIndex:ux_util_notif" json:"trigger"`
	PeriodKey  string    `gorm:"size:16;not null;uniqueIndex:ux_util_notif" json:"period_key"`
	SentAt     time.Time `gorm:"not null;autoCreateTime" json:"sent_at"`
}
