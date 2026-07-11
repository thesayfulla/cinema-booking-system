package config

import (
	"os"
)

// Config holds all application configuration.
// Values can be set via environment variables or defaults.
type Config struct {
	// Server configuration
	HTTPPort string

	// Redis configuration
	RedisAddr string

	// Storage backend: "memory" or "redis"
	StorageBackend string
}

// NewConfig creates a new Config from environment variables with sensible defaults.
func NewConfig() *Config {
	return &Config{
		HTTPPort:       getEnv("HTTP_PORT", "8080"),
		RedisAddr:      getEnv("REDIS_ADDR", "localhost:6379"),
		StorageBackend: getEnv("STORAGE_BACKEND", "redis"),
	}
}

// getEnv returns the environment variable value or the default.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
