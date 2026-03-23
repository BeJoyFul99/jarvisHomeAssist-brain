package models

import "time"

// ChatRoom represents a chat room (family group or direct AI conversation).
type ChatRoom struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Name        string     `gorm:"size:255;not null" json:"name"`
	Type        string     `gorm:"size:32;not null" json:"type"` // "group" or "direct_ai"
	OwnerID     *uint      `json:"owner_id"`                     // nil for group, user ID for direct_ai
	LastMsgText *string    `gorm:"size:255" json:"last_msg_text"`
	LastMsgAt   *time.Time `json:"last_msg_at"`
	LastMsgBy   *string    `gorm:"size:255" json:"last_msg_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// ChatMessage represents a single message in a chat room.
type ChatMessage struct {
	ID         uint         `gorm:"primaryKey" json:"id"`
	RoomID     uint         `gorm:"index;not null" json:"room_id"`
	ThreadID   *uint        `gorm:"index" json:"thread_id"` // optional conversation thread
	SenderID   *uint        `json:"sender_id"`              // nil for AI messages
	SenderName string       `gorm:"size:255;not null" json:"sender_name"`
	Role       string       `gorm:"size:32;not null" json:"role"`                // "user", "assistant", "system"
	Status     string       `gorm:"size:32;not null;default:sent" json:"status"` // "sent", "delivered", "seen", "processed"
	Content    string       `gorm:"type:text;not null" json:"content"`
	Type       string       `gorm:"size:32;not null;default:text" json:"type"` // "text", "image", "voice"
	MediaURL   *string      `gorm:"size:512" json:"media_url"`
	ReplyToID  *uint        `json:"reply_to_id"`
	ReplyTo    *ChatMessage `gorm:"foreignKey:ReplyToID" json:"reply_to,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
}

// ChatReadReceipt tracks the last message a user has seen in a room.
type ChatReadReceipt struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	RoomID    uint      `gorm:"uniqueIndex:idx_room_user;not null" json:"room_id"`
	UserID    uint      `gorm:"uniqueIndex:idx_room_user;not null" json:"user_id"`
	LastRead  uint      `gorm:"not null" json:"last_read"` // message ID
	UpdatedAt time.Time `json:"updated_at"`
}
