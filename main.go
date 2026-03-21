package main

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"jarvishomeassist-brain/internal/config"
	"jarvishomeassist-brain/internal/database"
	"jarvishomeassist-brain/internal/handlers"
	"jarvishomeassist-brain/internal/middleware"
	"jarvishomeassist-brain/internal/sse"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println(err)
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[config] %v", err)
	}

	// Connect to PostgreSQL
	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("[db] %v", err)
	}
	log.Println("[db] connected to PostgreSQL")

	// Run migrations & seed
	if err := database.Migrate(db); err != nil {
		log.Fatalf("[db] %v", err)
	}
	handlers.SeedDefaultUsers(db)

	// Initialize the Gin router
	r := gin.Default()

	// ── Auth routes (public) ────────────────────────────────
	auth := &handlers.AuthHandler{DB: db, Cfg: cfg}
	r.POST("/auth/login", auth.Login)
	r.POST("/auth/pin-login", auth.PINLogin)
	r.POST("/auth/reset-password", auth.ResetPassword)

	// ── Protected route group (require valid JWT) ────────────
	protected := r.Group("/api/v1")
	protected.Use(middleware.JWTAuth(cfg.JWTSecret))

	// ── SSE hub for real-time events ─────────────────────────
	eventHub := sse.NewHub()

	// ── SSE stream (requires valid JWT) ──────────────────────
	sseHandler := &handlers.SSEHandler{Hub: eventHub}
	protected.GET("/events", sseHandler.Stream)
	protected.GET("/events/health", sseHandler.HealthStream)

	// ── User management (JWT + per-handler resource perm checks) ─────
	// Administrators and family_members with user:* perms can access these.
	adminUsers := &handlers.AdminUserHandler{DB: db, Hub: eventHub}
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

	// ── System status ───────────────────────────────────────
	statusHandler := &handlers.StatusHandler{}
	r.GET("/api/v1/status", statusHandler.Get)                       // single JSON snapshot
	protected.GET("/status/stream", statusHandler.Stream)             // SSE stream (every 3s)

	// ── Cron: purge soft-deleted users older than 30 days ────
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		// Run once at startup, then every 24h
		handlers.PurgeDeletedUsers(db)
		for range ticker.C {
			handlers.PurgeDeletedUsers(db)
		}
	}()

	// Start the server on port 5000
	r.Run(":" + cfg.Port)
}
