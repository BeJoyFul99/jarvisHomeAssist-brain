package models

import "time"

// SmartDevice represents a registered smart-home device.
type SmartDevice struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Name         string    `gorm:"size:128;not null" json:"name"`
	Room         string    `gorm:"size:64;not null" json:"room"`
	DeviceType   string    `gorm:"size:32;not null;default:light" json:"device_type"` // light, thermostat, camera, speaker, sensor
	Brand        string    `gorm:"size:32;not null;default:wiz" json:"brand"`         // wiz, tuya, hue, etc.
	Model        string    `gorm:"size:64" json:"model"`                              // e.g. "WiZ A60 Color"
	IP           string    `gorm:"size:45;not null" json:"ip"`                        // IPv4 or IPv6
	MAC          string    `gorm:"size:17" json:"mac"`                                // AA:BB:CC:DD:EE:FF
	FirmwareVer  string    `gorm:"size:32" json:"firmware_ver"`
	Online       bool      `gorm:"default:false" json:"online"`
	State        JSON      `gorm:"type:jsonb;default:'{}'" json:"state"` // device-specific state blob
	Metadata     JSON      `gorm:"type:jsonb;default:'{}'" json:"metadata"` // extra info (module name, signal, etc.)
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
