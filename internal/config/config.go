package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all application configuration read from the environment.
type Config struct {
	Port              string
	DatabaseURL       string
	JWTSecret         string
	JWTExpiry         time.Duration
	CFWorkerURL       string // Cloudflare Worker AI endpoint (optional)
	CFWorkerSecret    string // Shared secret for CF Worker auth (optional)
	CFAccountID       string // Cloudflare Account ID (for Analytics Engine queries)
	CFAPIToken        string // Cloudflare API Token with Analytics read permissions
	UploadBaseDir     string // Base directory for file uploads (default: ./uploads)
	UploadChatSubdir  string // Subdirectory for chat uploads (default: chat)
	VAPIDPublicKey    string // VAPID public key for Web Push (optional)
	VAPIDPrivateKey   string // VAPID private key for Web Push (optional)
	VAPIDContact      string // VAPID contact email (e.g. mailto:admin@example.com)
}

// Load reads configuration from environment variables.
// Required variables will cause a fatal error if missing.
func Load() (*Config, error) {
	// Fall back to building from individual POSTGRES_* vars (docker-compose, .env, etc.)
	dbURL := ""
	pgHost := os.Getenv("POSTGRES_HOST")
	if pgHost != "" {
		user := envOrDefault("POSTGRES_USER", "postgres")
		pass := envOrDefault("POSTGRES_PASSWORD", "postgres")
		db := envOrDefault("POSTGRES_DB", "postgres")
		port := envOrDefault("POSTGRES_PORT", "5432")
		dbURL = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", pgHost, port, user, pass, db)
	}
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL or POSTGRES_HOST environment variable is required")
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET environment variable is required")
	}

	expiryHours, _ := strconv.Atoi(envOrDefault("JWT_EXPIRY_HOURS", "24"))

	return &Config{
		Port:             envOrDefault("BRAIN_PORT", "5000"),
		DatabaseURL:      dbURL,
		JWTSecret:        jwtSecret,
		JWTExpiry:        time.Duration(expiryHours) * time.Hour,
		CFWorkerURL:      os.Getenv("CF_WORKER_URL"),
		CFWorkerSecret:   os.Getenv("CF_WORKER_SECRET"),
		CFAccountID:      os.Getenv("CF_ACCOUNT_ID"),
		CFAPIToken:       os.Getenv("CF_API_TOKEN"),
		UploadBaseDir:    envOrDefault("UPLOAD_BASE_DIR", "./uploads"),
		UploadChatSubdir: envOrDefault("UPLOAD_CHAT_SUBDIR", "chat"),
		VAPIDPublicKey:   os.Getenv("VAPID_PUBLIC_KEY"),
		VAPIDPrivateKey:  os.Getenv("VAPID_PRIVATE_KEY"),
		VAPIDContact:     envOrDefault("VAPID_CONTACT", "mailto:admin@homelab.local"),
	}, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
