package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"jarvishomeassist-brain/internal/config"
	"jarvishomeassist-brain/internal/database"
	"jarvishomeassist-brain/internal/handlers"
	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/middleware"
	"jarvishomeassist-brain/internal/sse"
	"jarvishomeassist-brain/internal/workers"
	"jarvishomeassist-brain/internal/ws"
)

func main() {
	// Load .env if present (optional — Docker injects env vars via env_file)
	_ = godotenv.Load()

	dev_mode := ""
	if os.Getenv("DEV_MODE") == "true" {
		dev_mode = "Developement"
	} else {
		dev_mode = "Production"
	}
	log.Printf("Starting Jarvis Home Assist Brain in [%s] mode...\n", dev_mode)

	// Initialize file logger
	logDir := os.Getenv("LOG_DIR")
	if logDir == "" {
		logDir = "./logs"
	}
	appLogger, err := logger.New(logDir)
	if err != nil {
		log.Fatalf("[logger] %v", err)
	}
	defer appLogger.Close()
	appLogger.Info("system", "Jarvis Home Assist Brain starting in "+dev_mode+" mode")

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[config] %v", err)
	}
	appLogger.Info("config", "Configuration loaded")

	// Connect to PostgreSQL
	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("[db] %v", err)
	}
	appLogger.Info("db", "Connected to PostgreSQL")

	// Run migrations & seed
	if err := database.Migrate(db, appLogger); err != nil {
		log.Fatalf("[db] %v", err)
	}

	appLogger.Info("db", "Migrations complete")
	handlers.SeedDefaultUsers(db, appLogger)
	handlers.SeedDefaultWifiNetworks(db, appLogger)
	handlers.SeedDefaultDevices(db, appLogger)
	handlers.SeedDefaultSettings(db)
	handlers.SeedDefaultChatRooms(db, appLogger)

	// Initialize the Gin router
	r := gin.Default()

	// ── Static file serving (uploads) ────────────────────────
	r.Static("/uploads", cfg.UploadBaseDir)

	// ── Auth routes (public) ────────────────────────────────
	auth := &handlers.AuthHandler{DB: db, Cfg: cfg}
	r.POST("/auth/login", auth.Login)
	r.POST("/auth/pin-login", auth.PINLogin)
	r.POST("/auth/reset-password", auth.ResetPassword)
	r.POST("/auth/refresh", auth.RefreshToken)

	// Logout requires a valid JWT to identify the user
	logoutGroup := r.Group("/auth")
	logoutGroup.Use(middleware.JWTAuth(cfg.JWTSecret, db))
	logoutGroup.POST("/logout", auth.Logout)

	// ── Protected route group (require valid JWT) ────────────
	protected := r.Group("/api/v1")
	protected.Use(middleware.JWTAuth(cfg.JWTSecret, db))
	protected.Use(middleware.RateLimiter()) // Apply rate limiting to all protected routes

	// ── SSE hub for real-time events ─────────────────────────
	eventHub := sse.NewHub(appLogger)

	// ── SSE stream (requires valid JWT) ──────────────────────
	sseHandler := &handlers.SSEHandler{Hub: eventHub}
	protected.GET("/events", sseHandler.Stream)
	protected.GET("/events/health", sseHandler.HealthStream)

	// ── User management (JWT + per-handler resource perm checks) ─────
	// Administrators and family_members with user:* perms can access these.
	adminUsers := &handlers.AdminUserHandler{DB: db, Hub: eventHub, Log: appLogger}
	admin := protected.Group("/admin")
	admin.Use(middleware.RequireRole("administrator", "family_member"))

	admin.GET("/users", adminUsers.ListUsers)
	admin.POST("/users", adminUsers.CreateUser)
	admin.PATCH("/users/:id", adminUsers.UpdateUser)
	admin.DELETE("/users/:id", adminUsers.DeleteUser)
	admin.POST("/users/:id/lock", adminUsers.LockUser)
	admin.POST("/users/:id/revoke", adminUsers.RevokeTokens)
	admin.POST("/users/:id/reset-password", adminUsers.RequestPasswordReset)
	admin.POST("/users/:id/regenerate-pin", adminUsers.RegeneratePIN)
	admin.POST("/users/:id/restore", adminUsers.RestoreUser)
	admin.GET("/permissions/schema", adminUsers.PermissionsSchema)
	admin.GET("/audit-logs", adminUsers.AuditLogs)

	// ── WiFi management ─────────────────────────────────────
	wifi := &handlers.WifiHandler{DB: db, Hub: eventHub}
	protected.GET("/wifi", wifi.List)                           // all users can list
	protected.GET("/wifi/:id/credentials", wifi.GetCredentials) // for QR codes
	admin.POST("/wifi", wifi.Create)
	admin.PATCH("/wifi/:id", wifi.Update)
	admin.POST("/wifi/:id/toggle", wifi.Toggle)
	admin.DELETE("/wifi/:id", wifi.Delete)

	// ── Smart device management ─────────────────────────────
	devices := &handlers.DeviceHandler{DB: db, Hub: eventHub, Log: appLogger}
	protected.GET("/devices", devices.List)                 // all users can list
	protected.GET("/devices/:id", devices.Get)              // single device
	protected.GET("/devices/:id/state", devices.State)      // poll live state from bulb
	protected.POST("/devices/:id/control", devices.Control) // send command (on/off/brightness/etc.)
	protected.GET("/devices/scenes", devices.Scenes)        // available WiZ scenes
	admin.POST("/devices", devices.Create)
	admin.PATCH("/devices/:id", devices.Update)
	admin.DELETE("/devices/:id", devices.Delete)
	admin.POST("/devices/discover", devices.Discover) // scan network for WiZ bulbs

	// ── User preferences (per-user, any authenticated user) ─
	prefs := &handlers.PreferencesHandler{DB: db}
	protected.GET("/preferences", prefs.Get)
	protected.PUT("/preferences", prefs.Update)

	// ── Settings ────────────────────────────────────────────
	settings := &handlers.SettingsHandler{DB: db, Hub: eventHub}
	protected.GET("/settings", settings.List)
	admin.PUT("/settings", settings.Update)

	// ── Energy management ────────────────────────────────────
	energy := &handlers.EnergyHandler{DB: db, Hub: eventHub}
	protected.GET("/energy/today", energy.Today)
	protected.GET("/energy", energy.Range)
	protected.GET("/energy/summary", energy.Summary)
	protected.GET("/energy/rates", energy.ListRates)
	protected.GET("/energy/budget", energy.GetBudget)
	admin.POST("/energy", energy.Record)
	admin.POST("/energy/batch", energy.BatchRecord)
	admin.POST("/energy/rates", energy.UpsertRate)
	admin.DELETE("/energy/rates/:id", energy.DeleteRate)
	admin.POST("/energy/budget", energy.SetBudget)

	// ── Server logs (admin only) ────────────────────────────
	logsHandler := &handlers.LogsHandler{Logger: appLogger}
	admin.GET("/logs", logsHandler.List)
	admin.GET("/logs/stream", logsHandler.Stream)

	// ── AI usage analytics (admin only) ─────────────────────
	aiUsage := &handlers.AIUsageHandler{Cfg: cfg, Log: appLogger}
	admin.GET("/ai-usage/summary", aiUsage.Summary)
	admin.GET("/ai-usage/by-model", aiUsage.ByModel)
	admin.GET("/ai-usage/errors", aiUsage.Errors)
	admin.GET("/ai-usage/today", aiUsage.Today)
	admin.GET("/ai-usage/config", aiUsage.Config)

	// ── Chat (real-time messaging + AI) ─────────────────────
	wsHub := ws.NewHub(appLogger)
	// Create notifHandler early so we can wire chat → notification delivery
	notifHandler := &handlers.NotificationHandler{DB: db, WSHub: wsHub, Cfg: cfg, Log: appLogger}
	chat := &handlers.ChatHandler{
		DB: db, WSHub: wsHub, Cfg: cfg, Log: appLogger,
		Notify: notifHandler.DeliverChatNotification,
	}
	protected.GET("/chat/rooms", chat.ListRooms)
	protected.POST("/chat/rooms", chat.CreateRoom)
	protected.GET("/chat/users", chat.ListUsers)
	// Issue short-lived tickets for WebSocket handshake
	protected.POST("/chat/authorize", chat.Authorize)
	protected.GET("/chat/rooms/:id/messages", chat.GetMessages)
	protected.POST("/chat/rooms/:id/messages", chat.SendMessage)
	protected.POST("/chat/rooms/:id/seen", chat.MarkSeen)
	protected.DELETE("/chat/rooms/:id/messages", chat.ClearMessages)
	protected.PUT("/chat/rooms/:id/messages/:msgId", chat.EditMessage)
	protected.DELETE("/chat/rooms/:id/messages/:msgId", chat.DeleteMessage)
	// WS endpoint: outside protected group (browsers can't send auth headers on WS upgrade)
	// Auth is handled via ?token= query param inside the handler
	r.GET("/api/v1/chat/ws", chat.WebSocket)

	// ── Announcements ──────────────────────────────────────
	announcements := &handlers.AnnouncementHandler{
		DB: db, Hub: eventHub, WSHub: wsHub, Cfg: cfg, Log: appLogger,
		Notify: notifHandler.DeliverChatNotification,
	}
	protected.GET("/announcements", announcements.List)
	protected.GET("/announcements/:id", announcements.Get)
	protected.POST("/announcements/:id/read", announcements.MarkRead)
	admin.POST("/announcements", announcements.Create)
	admin.PATCH("/announcements/:id", announcements.Update)
	admin.DELETE("/announcements/:id", announcements.Delete)
	admin.GET("/announcements/:id/reads", announcements.ReadReceipts)

	// ── Notifications & Push ────────────────────────────────
	protected.GET("/notifications", notifHandler.List)
	protected.GET("/notifications/unread-count", notifHandler.UnreadCount)
	protected.POST("/notifications", notifHandler.Create)
	protected.PATCH("/notifications/:id/read", notifHandler.MarkRead)
	protected.POST("/notifications/read-all", notifHandler.MarkAllRead)
	protected.DELETE("/notifications/:id", notifHandler.Delete)
	protected.DELETE("/notifications", notifHandler.ClearAll)
	protected.GET("/push/vapid-key", notifHandler.VAPIDKey)
	protected.POST("/push/subscribe", notifHandler.Subscribe)
	protected.DELETE("/push/subscribe", notifHandler.Unsubscribe)

	// ── System status ───────────────────────────────────────
	statusHandler := &handlers.StatusHandler{Hub: eventHub, Log: appLogger}
	r.GET("/api/v1/status", statusHandler.Get)            // single JSON snapshot
	protected.GET("/status/stream", statusHandler.Stream) // SSE stream (every 3s)
	statusHandler.StartStatusTicker()                     // push status through SSE hub every 3s

	// ── Cron: purge soft-deleted users older than 30 days ────
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		// Run once at startup, then every 24h
		handlers.PurgeDeletedUsers(db, appLogger)
		for range ticker.C {
			handlers.PurgeDeletedUsers(db, appLogger)
		}
	}()

	// ── Cron: reminder worker (fires scheduled notifications) ──
	go workers.StartReminderWorker(db, wsHub, notifHandler.SendPush, appLogger)

	// Start the server with explicit timeouts that won't kill WebSocket connections.
	// ReadHeaderTimeout guards the initial handshake; no ReadTimeout/WriteTimeout
	// so long-lived WebSocket and SSE connections are not prematurely closed.
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	appLogger.Info("system", "Server listening on port "+cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[server] %v", err)
	}
}
