package models

import (
	"time"

	"gorm.io/gorm"
)

// Announcement represents a household announcement published by an admin.
type Announcement struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	AuthorID  uint           `gorm:"not null" json:"author_id"`
	Author    User           `gorm:"foreignKey:AuthorID" json:"author,omitempty"`
	Title     string         `gorm:"size:255;not null" json:"title"`
	Body      string         `gorm:"type:text;not null" json:"body"`
	Priority  string         `gorm:"size:32;not null;default:'normal'" json:"priority"` // low, normal, high, urgent
	Category  string         `gorm:"size:64;not null;default:'general'" json:"category"` // general, maintenance, security, event
	Pinned    bool           `gorm:"not null;default:false" json:"pinned"`
	EditedAt  *time.Time     `json:"edited_at"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// Virtual fields populated by handler
	ReadCount  int                `gorm:"-" json:"read_count"`
	TotalUsers int                `gorm:"-" json:"total_users"`
	Reads      []AnnouncementRead `gorm:"-" json:"reads,omitempty"`
}

// AnnouncementRead tracks which users have read an announcement.
type AnnouncementRead struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	AnnouncementID uint      `gorm:"index;not null" json:"announcement_id"`
	UserID         uint      `gorm:"index;not null" json:"user_id"`
	User           User      `gorm:"foreignKey:UserID" json:"user,omitempty"`
	ReadAt         time.Time `gorm:"not null" json:"read_at"`
}
