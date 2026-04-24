package models

import "time"

// UtilityBillMeter — meter readings parsed from a bill. See spec §2.4.
type UtilityBillMeter struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	BillID           uint      `gorm:"not null;index" json:"bill_id"`
	MeterType        string    `gorm:"size:16;not null" json:"meter_type"`
	MeterNumber      string    `gorm:"size:32;not null" json:"meter_number"`
	PreviousReading  string    `gorm:"size:16" json:"previous_reading"`
	PreviousReadDate time.Time `json:"previous_read_date"`
	CurrentReading   string    `gorm:"size:16" json:"current_reading"`
	CurrentReadDate  time.Time `json:"current_read_date"`
	Usage            float64   `gorm:"type:decimal(12,3)" json:"usage"`
	Multiplier       int       `gorm:"not null;default:1" json:"multiplier"`
	CreatedAt        time.Time `json:"created_at"`
}
