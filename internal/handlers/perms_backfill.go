package handlers

import (
	"fmt"

	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"
)

// PermsBackfillKey marks that the one-time resource-permission backfill ran.
const PermsBackfillKey = "perms_backfill_v1"

// BackfillEmptyResourcePerms grants role-default resource permissions to users
// whose resource_perms is NULL or '[]'. Rows created before the resource-perm
// system existed were left with the column default '[]', which locked
// non-admin users out of every perm-gated feature.
//
// Runs once: guarded by a settings flag so perms an admin later empties on
// purpose are not silently re-granted.
func BackfillEmptyResourcePerms(db *gorm.DB, log *logger.Logger) {
	var count int64
	db.Model(&models.Setting{}).Where("key = ?", PermsBackfillKey).Count(&count)
	if count > 0 {
		return
	}

	var users []models.User
	if err := db.Find(&users).Error; err != nil {
		if log != nil {
			log.Error("seed", fmt.Sprintf("perms backfill: failed to list users: %v", err))
		}
		return
	}

	healed := 0
	for _, u := range users {
		if len(u.GetResourcePerms()) > 0 {
			continue
		}
		if err := db.Model(&models.User{}).Where("id = ?", u.ID).
			Update("resource_perms", models.DefaultPermsJSON(u.Role)).Error; err != nil {
			if log != nil {
				log.Error("seed", fmt.Sprintf("perms backfill: user %s: %v", u.Email, err))
			}
			continue
		}
		healed++
	}

	if err := db.Create(&models.Setting{Key: PermsBackfillKey, Value: "done"}).Error; err != nil {
		if log != nil {
			log.Error("seed", fmt.Sprintf("perms backfill: failed to record flag: %v", err))
		}
		return
	}
	if log != nil && healed > 0 {
		log.Info("seed", fmt.Sprintf("perms backfill: granted role-default permissions to %d user(s)", healed))
	}
}
