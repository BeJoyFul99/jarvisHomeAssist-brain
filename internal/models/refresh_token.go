package models

import (
	"time"
)

// RefreshToken tracks user refresh tokens to support rotation and reuse detection.
type RefreshToken struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"not null;index" json:"user_id"`
	TokenHash string    `gorm:"size:512;uniqueIndex;not null" json:"-"`
	IsRevoked bool      `gorm:"not null;default:false" json:"is_revoked"`
	ExpiresAt time.Time `gorm:"not null;index" json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
