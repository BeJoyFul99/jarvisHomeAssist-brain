package models

import "time"

// WifiNetwork represents a managed home WiFi network.
type WifiNetwork struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	SSID        string    `gorm:"size:64;not null;uniqueIndex" json:"ssid"`
	Password    string    `gorm:"size:128;not null" json:"password"`
	Security    string    `gorm:"size:16;not null;default:WPA2" json:"security"`
	Band        string    `gorm:"size:16;not null;default:5 GHz" json:"band"`
	Description string    `gorm:"size:255" json:"description"`
	IsGuest     bool      `gorm:"default:false" json:"is_guest"`
	Enabled     bool      `gorm:"default:true" json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
