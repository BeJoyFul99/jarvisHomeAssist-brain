package handlers

import (
	"context"
	"log"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
)

type seedUser struct {
	Email         string
	DisplayName   string
	Password      string
	Role          models.Role
	Permissions   models.Permissions
	ResourcePerms datatypes.JSON
}

// SeedDefaultUsers ensures the default accounts exist.
// Uses FirstOrCreate so it is safe to call on every startup.
func SeedDefaultUsers(db *gorm.DB) {
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
			Email:         "admin@homelab.local",
			DisplayName:   "Jarvis Admin",
			Password:      "admin1234",
			Role:          models.RoleAdmin,
			Permissions:   models.AdminAll,
			ResourcePerms: models.DefaultPermsJSON(models.RoleAdmin),
		},
		{
			Email:         "family@homelab.local",
			DisplayName:   "Family User",
			Password:      "family1234",
			Role:          models.RoleFamilyMember,
			Permissions:   0,
			ResourcePerms: models.DefaultPermsJSON(models.RoleFamilyMember),
		},
		{
			Email:         "guest@homelab.local",
			DisplayName:   "Guest User",
			Password:      "guest1234",
			Role:          models.RoleGuest,
			Permissions:   0,
			ResourcePerms: models.DefaultPermsJSON(models.RoleGuest),
		},
	}

	for _, s := range defaults {
		var existing models.User
		result := db.WithContext(ctx).Where("email = ?", s.Email).First(&existing)
		if result.Error == nil {
			continue // already exists
		}

		u := models.User{
			Email:         s.Email,
			DisplayName:   s.DisplayName,
			Role:          s.Role,
			Permissions:   s.Permissions,
			ResourcePerms: s.ResourcePerms,
		}
		if err := u.SetPassword(s.Password); err != nil {
			log.Printf("[seed] failed to hash password for %s: %v", s.Email, err)
			continue
		}
		if err := db.WithContext(ctx).Create(&u).Error; err != nil {
			log.Printf("[seed] failed to create %s: %v", s.Email, err)
			continue
		}
		log.Printf("[seed] created user %s (%s)", s.Email, s.Role)
	}
}
