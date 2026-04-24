package models

import (
	"time"

	"gorm.io/datatypes"
)

// Property represents a service address / utility account. See spec §2.1.
type Property struct {
	ID            uint           `gorm:"primaryKey" json:"id"`
	Name          string         `gorm:"size:128;not null" json:"name"`
	Address       string         `gorm:"size:255;not null" json:"address"`
	MeterNumbers  datatypes.JSON `gorm:"type:jsonb" json:"meter_numbers"`
	AccountNumber string         `gorm:"size:32" json:"account_number"`
	Provider      string         `gorm:"size:64;not null;default:powerstream" json:"provider"`
	RateClass     string         `gorm:"size:64" json:"rate_class"`
	IsActive      bool           `gorm:"not null;default:true" json:"is_active"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}
