package models

import (
	"time"

	"gorm.io/datatypes"
)

// DebtProfile stores one user's Debt Rescue Mode inputs (income, fixed
// expenses, available cash and the list of debts) as a flexible JSON document.
// One row per user; all access is owner-scoped by UserID.
//
// Only the inputs are persisted — the recovery plan itself is recomputed
// deterministically on demand by internal/debt, so a stored plan can never
// drift out of sync with the numbers it was derived from.
type DebtProfile struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	UserID    uint           `gorm:"uniqueIndex;not null" json:"user_id"`
	Data      datatypes.JSON `gorm:"type:jsonb;default:'{}'" json:"data"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}
