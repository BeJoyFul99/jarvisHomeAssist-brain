package database

import (
	"fmt"
	"log"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"jarvishomeassist-brain/internal/models"
)

// Connect opens a PostgreSQL connection via GORM with pooling defaults.
func Connect(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
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
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&models.User{}, &models.AuditLog{}, &models.WifiNetwork{}, &models.SmartDevice{}, &models.EnergyReading{}, &models.EnergyRate{}, &models.EnergyBudget{}, &models.Setting{}, &models.ChatRoom{}, &models.ChatMessage{}, &models.ChatReadReceipt{}); err != nil {
		return fmt.Errorf("auto-migrate: %w", err)
	}
	log.Println("[db] migrations complete")
	return nil
}
