package models

import (
	"time"
)

// ChatRoom represents a chat room (family group or direct AI conversation).
type ChatRoom struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Name    string `gorm:"size:255;not null" json:"name"`
	Type    string `gorm:"size:32;not null" json:"type"` // "group", "direct_ai", "DM"
	OwnerID *uint  `json:"owner_id"`                     // User ID for direct_ai/Owner

	// Relationships
	Participants []User `gorm:"many2many:chat_room_participants;" json:"participants,omitempty"`

	// Denormalized fields for quick preview in room lists
	LastMsgText *string    `gorm:"size:255" json:"last_msg_text"`
	LastMsgAt   *time.Time `json:"last_msg_at"`
	LastMsgBy   *string    `gorm:"size:255" json:"last_msg_by"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ChatMessage represents a single message in a chat room.
type ChatMessage struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// Foreign Key to Room
	RoomID uint     `gorm:"index;not null" json:"room_id"`
	Room   ChatRoom `gorm:"foreignKey:RoomID" json:"-"`

	ThreadID *uint `gorm:"index" json:"thread_id"`

	// Foreign Key to User (Sender)
	SenderID *uint `gorm:"index" json:"sender_id"`
	Sender   *User `gorm:"foreignKey:SenderID" json:"sender,omitempty"`

	Role     string  `gorm:"size:32;not null" json:"role"`                // "user", "assistant", "system"
	Status   string  `gorm:"size:32;not null;default:sent" json:"status"` // "sent", "delivered", "seen"
	Content  string  `gorm:"type:text;not null" json:"content"`
	Type     string  `gorm:"size:32;not null;default:text" json:"type"` // "text", "image", "voice"
	MediaURL *string `gorm:"size:512" json:"media_url"`

	// Self-referencing Foreign Key for Replies
	ReplyToID *uint        `json:"reply_to_id"`
	ReplyTo   *ChatMessage `gorm:"foreignKey:ReplyToID" json:"reply_to,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// ChatReadReceipt tracks the last message a user has seen in a room.
type ChatReadReceipt struct {
	ID uint `gorm:"primaryKey" json:"id"`

	RoomID uint `gorm:"uniqueIndex:idx_room_user;not null" json:"room_id"`
	UserID uint `gorm:"uniqueIndex:idx_room_user;not null" json:"user_id"`

	// References
	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`

	LastRead  uint      `gorm:"not null" json:"last_read"` // ID of the last ChatMessage seen
	UpdatedAt time.Time `json:"updated_at"`
}
