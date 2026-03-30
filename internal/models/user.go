package models

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Role represents a user's access tier, matching the frontend JWT contract.
type Role string

const (
	RoleAdmin        Role = "administrator"
	RoleFamilyMember Role = "family_member"
	RoleGuest        Role = "guest"
	RoleAssistant    Role = "assistant" // For the AI assistant user
)

// Permissions is a bitmask for admin-level operations (user management, etc.).
type Permissions uint64

const (
	PermManageUsers  Permissions = 1 << iota // create/edit/delete users
	PermLockUsers                            // lock/unlock accounts
	PermRevokeTokens                         // force-revoke JWT tokens
	PermViewUsers                            // list and view user details
)

// AdminAll is the full permission set granted to administrators.
const AdminAll = PermManageUsers | PermLockUsers | PermRevokeTokens | PermViewUsers

// Has checks whether p includes every bit in required.
func (p Permissions) Has(required Permissions) bool {
	return p&required == required
}

// ── Resource permission system (IAM-style) ─────────────────

// ValidResourcePerms is the full list of assignable resource permissions.
var ValidResourcePerms = []string{
	"ui:view",
	"network:view", "network:manage",
	"smart_device:view", "smart_device:control", "smart_device:group",
	"media:view", "media:manage",
	"camera:view", "camera:manage",
	"user:view", "user:create_guest", "user:edit_perms",
	"user:regenerate_pin", "user:delete_guest", "user:lock",
}

// PermCategories groups permissions by UI category for the admin panel.
var PermCategories = map[string][]string{
	"Dashboard":       {"ui:view"},
	"Network":         {"network:view", "network:manage"},
	"Smart Devices":   {"smart_device:view", "smart_device:control", "smart_device:group"},
	"Media":           {"media:view", "media:manage"},
	"Cameras":         {"camera:view", "camera:manage"},
	"User Management": {"user:view", "user:create_guest", "user:edit_perms", "user:regenerate_pin", "user:delete_guest", "user:lock"},
}

// PermCategoryOrder defines the display order for permission categories.
var PermCategoryOrder = []string{
	"Dashboard", "Network", "Smart Devices", "Media", "Cameras", "User Management",
}

// DefaultPermsForRole returns the default resource permissions for a given role.
func DefaultPermsForRole(role Role) []string {
	switch role {
	case RoleAdmin:
		return ValidResourcePerms
	case RoleFamilyMember:
		return []string{
			"ui:view", "network:view",
			"smart_device:view", "smart_device:control",
			"media:view", "media:manage",
			"camera:view",
		}
	case RoleAssistant:
		return []string{
			"smart_device:view", "smart_device:control",
			"media:view",
		}
	case RoleGuest:
		return []string{"ui:view", "smart_device:view", "media:view"}
	default:
		return []string{"ui:view"}
	}
}

// DefaultPermsJSON returns default resource permissions as JSON bytes.
func DefaultPermsJSON(role Role) datatypes.JSON {
	perms := DefaultPermsForRole(role)
	b, _ := json.Marshal(perms)
	return datatypes.JSON(b)
}

// ── PIN generation ─────────────────────────────────────────

// GeneratePIN generates a cryptographically random 6-digit PIN.
func GeneratePIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// User is the GORM model for the users table.
type User struct {
	ID            uint           `gorm:"primaryKey" json:"id"`
	Email         string         `gorm:"uniqueIndex;size:255;not null" json:"email"`
	DisplayName   string         `gorm:"size:255;not null" json:"display_name"`
	Password      string         `gorm:"not null" json:"-"`
	GuestPIN      string         `gorm:"size:255" json:"-"`
	Role          Role           `gorm:"size:50;not null;default:'family_member'" json:"role"`
	Permissions   Permissions    `gorm:"not null;default:0" json:"permissions"`
	ResourcePerms datatypes.JSON `gorm:"type:jsonb;default:'[]'" json:"resource_perms"`
	PermExpiresAt *time.Time     `json:"perm_expires_at"`
	Phone         string         `gorm:"size:50" json:"phone"`
	IsLocked      bool           `gorm:"not null;default:false" json:"is_locked"`
	JWTToken      string         `gorm:"size:512" json:"-"`          // bcrypt hash of current access token (empty = revoked)
	AccessCount   int64          `gorm:"not null;default:0" json:"access_count"`
	RefreshToken  string         `gorm:"size:512" json:"-"`          // bcrypt hash of current refresh token (empty = revoked)
	FCMToken      string         `gorm:"size:512" json:"-"`
	LastLoginAt   *time.Time     `json:"last_login_at"`
	// Password reset token and expiry used for password reset flows.
	PasswordResetToken  string     `gorm:"size:512" json:"-"`
	PasswordResetExpiry *time.Time `json:"-"`
	// Per-user UI preferences (theme, notifications, etc.)
	Preferences datatypes.JSON `gorm:"type:jsonb;default:'{}'" json:"preferences"`
	// Tracks which admin created this user.
	CreatedByID   uint           `gorm:"default:0" json:"created_by_id"`
	CreatedByName string         `gorm:"size:255" json:"created_by_name"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

// SetPassword hashes and stores the plain-text password.
func (u *User) SetPassword(plain string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.Password = string(hash)
	return nil
}

// CheckPassword compares a plain-text candidate against the stored hash.
func (u *User) CheckPassword(plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(plain)) == nil
}

// SetPIN hashes and stores a 6-digit guest PIN.
func (u *User) SetPIN(plain string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.GuestPIN = string(hash)
	return nil
}

// CheckPIN compares a plain-text PIN against the stored hash.
func (u *User) CheckPIN(plain string) bool {
	if u.GuestPIN == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(u.GuestPIN), []byte(plain)) == nil
}

// IsGuestExpired returns true if the guest's access has expired.
func (u *User) IsGuestExpired() bool {
	if u.PermExpiresAt == nil {
		return false
	}
	return u.PermExpiresAt.Before(time.Now())
}

// GetResourcePerms deserializes the JSON permission array.
func (u *User) GetResourcePerms() []string {
	var perms []string
	if u.ResourcePerms == nil {
		return perms
	}
	_ = json.Unmarshal(u.ResourcePerms, &perms)
	return perms
}

// HasResourcePerm checks if the user has a specific resource permission.
func (u *User) HasResourcePerm(perm string) bool {
	if u.Role == RoleAdmin {
		return true
	}
	if u.IsGuestExpired() {
		return false
	}
	for _, p := range u.GetResourcePerms() {
		if p == perm {
			return true
		}
	}
	return false
}

// IsValidResourcePerm checks if a permission string is recognized.
func IsValidResourcePerm(perm string) bool {
	for _, v := range ValidResourcePerms {
		if v == perm {
			return true
		}
	}
	return false
}
