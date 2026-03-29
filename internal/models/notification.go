package models

import (
	"time"

	"gorm.io/gorm"
)

// Notification represents a persisted notification for a user.
type Notification struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	UserID      uint           `gorm:"index;not null" json:"user_id"`
	Title       string         `gorm:"size:255;not null" json:"title"`
	Message     string         `gorm:"type:text;not null" json:"message"`
	Type        string         `gorm:"size:32;not null;default:'info'" json:"type"`       // info, warning, error, success
	Category    string         `gorm:"size:64;not null;default:'system'" json:"category"` // system, reminder, device, energy
	Read        bool           `gorm:"not null;default:false" json:"read"`
	ActionURL   *string        `gorm:"size:512" json:"action_url"`
	ScheduledAt *time.Time     `gorm:"index" json:"scheduled_at"` // for reminders: when to fire
	Fired       bool           `gorm:"not null;default:false" json:"fired"`
	ExpiresAt   *time.Time     `json:"expires_at"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

// PushSubscription stores a Web Push API subscription for a user/device pair.
type PushSubscription struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	Endpoint  string    `gorm:"type:text;not null;uniqueIndex" json:"endpoint"`
	KeyP256dh string    `gorm:"type:text;not null" json:"key_p256dh"`
	KeyAuth   string    `gorm:"type:text;not null" json:"key_auth"`
	UserAgent string    `gorm:"size:512" json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
