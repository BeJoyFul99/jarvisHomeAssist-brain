package models

import "time"

// UtilityBillLineItem — every charge line on a bill. See spec §2.3.
type UtilityBillLineItem struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	BillID      uint      `gorm:"not null;index" json:"bill_id"`
	UtilityType string    `gorm:"size:16;not null" json:"utility_type"`
	Category    string    `gorm:"size:32;not null" json:"category"`
	Description string    `gorm:"size:255;not null" json:"description"`
	UsageAmount *float64  `gorm:"type:decimal(12,3)" json:"usage_amount"`
	UsageUnit   *string   `gorm:"size:16" json:"usage_unit"`
	Rate        *float64  `gorm:"type:decimal(10,5)" json:"rate"`
	Amount      float64   `gorm:"type:decimal(12,2);not null" json:"amount"`
	CreatedAt   time.Time `json:"created_at"`
}
