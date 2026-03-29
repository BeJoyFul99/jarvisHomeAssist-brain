package workers

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/ws"
)

// StartReminderWorker polls for due reminders every 30 seconds and delivers them.
func StartReminderWorker(db *gorm.DB, wsHub *ws.Hub, pushFn func(uint, string, string), log *logger.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	log.Info("worker", "reminder worker started (30s interval)")

	// Run once at startup then on each tick
	fireReminders(db, wsHub, pushFn, log)
	for range ticker.C {
		fireReminders(db, wsHub, pushFn, log)
	}
}

func fireReminders(db *gorm.DB, wsHub *ws.Hub, pushFn func(uint, string, string), log *logger.Logger) {
	var reminders []models.Notification
	db.Where("scheduled_at <= ? AND fired = false AND deleted_at IS NULL", time.Now()).Find(&reminders)

	for _, r := range reminders {
		db.Model(&r).Update("fired", true)

		// Real-time WebSocket delivery
		wsHub.SendToUser(r.UserID, ws.Message{
			Type: "notification:new",
			Data: r,
		})

		// Web Push
		pushFn(r.UserID, r.Title, r.Message)

		log.Info("worker", fmt.Sprintf("fired reminder #%d for user %d: %s", r.ID, r.UserID, r.Title))
	}

	// Cleanup: purge expired notifications older than 30 days
	db.Where("expires_at IS NOT NULL AND expires_at < ?", time.Now()).Delete(&models.Notification{})
}
