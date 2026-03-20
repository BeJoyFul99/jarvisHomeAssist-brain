package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all application configuration read from the environment.
type Config struct {
	Port        string
	DatabaseURL string
	JWTSecret   string
	JWTExpiry   time.Duration
}

// Load reads configuration from environment variables.
// Required variables will cause a fatal error if missing.
func Load() (*Config, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		// Build from individual POSTGRES_* vars for backwards compat with docker-compose
		user := envOrDefault("POSTGRES_USER", "postgres")
		pass := envOrDefault("POSTGRES_PASSWORD", "postgres")
		db := envOrDefault("POSTGRES_DB", "postgres")
		host := envOrDefault("POSTGRES_HOST", "jarvis_memory")
		port := envOrDefault("POSTGRES_PORT", "5432")
		dbURL = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", host, port, user, pass, db)
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET environment variable is required")
	}

	expiryHours, _ := strconv.Atoi(envOrDefault("JWT_EXPIRY_HOURS", "24"))

	return &Config{
		Port:        envOrDefault("PORT", "5000"),
		DatabaseURL: dbURL,
		JWTSecret:   jwtSecret,
		JWTExpiry:   time.Duration(expiryHours) * time.Hour,
	}, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
