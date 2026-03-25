package handlers

import (
	"context"
	"fmt"
	"time"

	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type seedUser struct {
	ID            uint
	Email         string
	DisplayName   string
	Password      string
	Role          models.Role
	Permissions   models.Permissions
	ResourcePerms datatypes.JSON
}

// SeedDefaultUsers ensures the default accounts exist.
// Uses FirstOrCreate so it is safe to call on every startup.
func SeedDefaultUsers(db *gorm.DB, log *logger.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	defaults := []seedUser{
		{
			Email:         "joynerlee99@gmail.com",
			DisplayName:   "joynerlee",
			Password:      "991220",
			Role:          models.RoleAdmin,
			Permissions:   models.AdminAll,
			ResourcePerms: models.DefaultPermsJSON(models.RoleAdmin),
		},

		{
			ID:            AIUserID, // reserved ID for AI assistant
			Email:         "jarvis-assistant@system.local",
			DisplayName:   "Jarvis",
			Role:          models.RoleAssistant,
			Password:      "SYSTEM_ACCOUNT_NO_LOGIN",
			ResourcePerms: models.DefaultPermsJSON(models.RoleAssistant),
		},
	}

	for _, s := range defaults {
		var count int64
		db.WithContext(ctx).Model(&models.User{}).Where("email = ?", s.Email).Count(&count)
		if count > 0 {
			continue // already exists
		}

		u := models.User{
			Email:         s.Email,
			DisplayName:   s.DisplayName,
			Role:          s.Role,
			Permissions:   s.Permissions,
			ResourcePerms: s.ResourcePerms,
			ID:            s.ID,
		}
		if u.Role != models.RoleAssistant {
			if err := u.SetPassword(s.Password); err != nil {
				log.Error("seed", fmt.Sprintf("failed to hash password for %s: %v", s.Email, err))
				continue
			}
		} else {
			u.SetPassword(s.Password)
		}
		if err := db.WithContext(ctx).Create(&u).Error; err != nil {
			log.Error("seed", fmt.Sprintf("failed to create %s: %v", s.Email, err))
			continue
		}
		log.Info("seed", fmt.Sprintf("created user %s (%s)", s.Email, s.Role))
	}
}
