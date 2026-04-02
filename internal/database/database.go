package database

import (
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	applogger "jarvishomeassist-brain/internal/logger"

	"jarvishomeassist-brain/internal/models"
)

// Connect opens a PostgreSQL connection via GORM with pooling defaults.
func Connect(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("gorm open: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("underlying sql.DB: %w", err)
	}

	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	return db, nil
}

// Migrate runs AutoMigrate for all application models.
func Migrate(db *gorm.DB, log *applogger.Logger) error {
	if err := db.AutoMigrate(&models.User{}, &models.RefreshToken{}, &models.AuditLog{}, &models.WifiNetwork{}, &models.SmartDevice{}, &models.EnergyReading{}, &models.EnergyRate{}, &models.EnergyBudget{}, &models.Setting{}, &models.ChatRoom{}, &models.ChatMessage{}, &models.ChatReadReceipt{}, &models.Notification{}, &models.PushSubscription{}, &models.Announcement{}, &models.AnnouncementRead{}); err != nil {
		return fmt.Errorf("auto-migrate: %w", err)
	}
	log.Info("db", "migrations complete")
	return nil
}
