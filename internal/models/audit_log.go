package models

import "time"

// AuditLog records who performed an administrative action and on whom.
type AuditLog struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Action       string    `gorm:"size:100;not null;index" json:"action"`
	ActorID      uint      `gorm:"not null;index" json:"actor_id"`
	ActorEmail   string    `gorm:"size:255;not null" json:"actor_email"`
	ActorName    string    `gorm:"size:255" json:"actor_name"`
	TargetID     uint      `gorm:"not null;index" json:"target_id"`
	TargetEmail  string    `gorm:"size:255" json:"target_email"`
	TargetName   string    `gorm:"size:255" json:"target_name"`
	Details      string    `gorm:"type:text" json:"details,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Audit action constants.
const (
	AuditUserCreated        = "user:created"
	AuditUserUpdated        = "user:updated"
	AuditUserDeleted        = "user:deleted"
	AuditUserRestored       = "user:restored"
	AuditUserLocked         = "user:locked"
	AuditUserUnlocked       = "user:unlocked"
	AuditTokensRevoked      = "user:tokens_revoked"
	AuditPINRegenerated     = "user:pin_regenerated"
	AuditPasswordReset      = "user:password_reset_requested"
	AuditPermissionsChanged = "user:permissions_changed"
)
