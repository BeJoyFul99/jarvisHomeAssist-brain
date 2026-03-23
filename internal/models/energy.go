package models

import "time"

// EnergyReading stores hourly energy consumption data.
type EnergyReading struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Timestamp time.Time `gorm:"not null;index:idx_energy_ts" json:"timestamp"` // hour bucket (e.g. 2026-03-22 14:00:00)
	WattHours float64   `gorm:"not null;default:0" json:"watt_hours"`          // energy consumed in this hour (Wh)
	AvgWatts  float64   `gorm:"not null;default:0" json:"avg_watts"`           // average power draw (W) during this hour
	PeakWatts float64   `gorm:"default:0" json:"peak_watts"`                   // peak power draw (W) during this hour
	Source    string    `gorm:"size:64;not null;default:manual" json:"source"`  // manual, smart_meter, estimate
	CreatedAt time.Time `json:"created_at"`
}

// EnergyRate defines the electricity tariff for cost calculations.
type EnergyRate struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:64;not null" json:"name"`                    // e.g. "Peak", "Off-Peak", "Standard"
	PricePerKWh float64   `gorm:"not null" json:"price_per_kwh"`                   // cost per kWh in local currency
	Currency    string    `gorm:"size:8;not null;default:USD" json:"currency"`
	StartHour   int       `gorm:"not null;default:0" json:"start_hour"`            // 0-23 when this rate starts
	EndHour     int       `gorm:"not null;default:24" json:"end_hour"`             // 0-24 when this rate ends
	IsActive    bool      `gorm:"default:true" json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// EnergyBudget stores monthly energy budget settings.
type EnergyBudget struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	Month         int       `gorm:"not null" json:"month"`           // 1-12
	Year          int       `gorm:"not null" json:"year"`
	BudgetKWh     float64   `gorm:"not null;default:0" json:"budget_kwh"`
	BudgetAmount  float64   `gorm:"not null;default:0" json:"budget_amount"` // dollar amount
	Currency      string    `gorm:"size:8;not null;default:USD" json:"currency"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
