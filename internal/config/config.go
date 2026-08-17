// Package config loads and validates runtime configuration from the
// environment. Everything the service needs to run differently between
// environments is here; nothing else reads os.Getenv.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the complete runtime configuration.
type Config struct {
	Env  string // "development" or "production"
	HTTP HTTPConfig
	DB   DBConfig
	Hold HoldConfig
	Pay  PaymentConfig
	Log  LogConfig
}

// HTTPConfig configures the API server.
type HTTPConfig struct {
	Port            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
	MaxBodyBytes    int64
	// CORSOrigins lists allowed browser origins; "*" allows any.
	CORSOrigins []string
	// RateLimitRPS and RateLimitBurst throttle each client IP.
	RateLimitRPS   float64
	RateLimitBurst int
	StaticDir      string
}

// DBConfig configures the PostgreSQL connection pool.
type DBConfig struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
	AutoMigrate     bool
	SeedDemoData    bool
}

// HoldConfig configures reservation rules.
type HoldConfig struct {
	TTL                time.Duration
	MaxSeatsPerBooking int
	// BookingCutoff stops sales shortly before a screening starts.
	BookingCutoff time.Duration
	// SweepInterval is how often lapsed holds are reclaimed.
	SweepInterval time.Duration
	SweepBatch    int
}

// PaymentConfig selects and configures the payment provider.
type PaymentConfig struct {
	// Provider names the gateway implementation, e.g. "mock".
	Provider      string
	WebhookSecret string
	// PaymentWindow is how long seats stay held during checkout.
	PaymentWindow time.Duration
	// RefundCutoff blocks self-service refunds close to the screening.
	RefundCutoff time.Duration
	// EnableTestEndpoints exposes the simulated-gateway route used by the demo
	// UI. It must stay off outside development.
	EnableTestEndpoints bool
}

// LogConfig configures the structured logger.
type LogConfig struct {
	Level  string // debug, info, warn, error
	Format string // json or text
}

// Provider names.
const ProviderMock = "mock"

// Load reads configuration from the environment and validates it. It returns
// every problem it finds at once rather than failing one variable at a time.
func Load() (*Config, error) {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	env := getEnv("APP_ENV", "development")
	if env != "development" && env != "production" {
		fail("APP_ENV must be development or production, got %q", env)
	}

	cfg := &Config{
		Env: env,
		HTTP: HTTPConfig{
			Port:            getEnv("HTTP_PORT", "8080"),
			ReadTimeout:     getDuration("HTTP_READ_TIMEOUT", 10*time.Second, fail),
			WriteTimeout:    getDuration("HTTP_WRITE_TIMEOUT", 20*time.Second, fail),
			IdleTimeout:     getDuration("HTTP_IDLE_TIMEOUT", 60*time.Second, fail),
			RequestTimeout:  getDuration("HTTP_REQUEST_TIMEOUT", 8*time.Second, fail),
			ShutdownTimeout: getDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second, fail),
			MaxBodyBytes:    int64(getInt("HTTP_MAX_BODY_BYTES", 64*1024, fail)),
			CORSOrigins:     getList("CORS_ORIGINS", []string{"*"}),
			RateLimitRPS:    getFloat("RATE_LIMIT_RPS", 20, fail),
			RateLimitBurst:  getInt("RATE_LIMIT_BURST", 40, fail),
			StaticDir:       getEnv("STATIC_DIR", "static"),
		},
		DB: DBConfig{
			DSN:             getEnv("DATABASE_URL", "postgres://cinema:cinema@localhost:5432/cinema?sslmode=disable"),
			MaxConns:        int32(getInt("DB_MAX_CONNS", 20, fail)),
			MinConns:        int32(getInt("DB_MIN_CONNS", 2, fail)),
			MaxConnLifetime: getDuration("DB_MAX_CONN_LIFETIME", time.Hour, fail),
			MaxConnIdleTime: getDuration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute, fail),
			ConnectTimeout:  getDuration("DB_CONNECT_TIMEOUT", 10*time.Second, fail),
			AutoMigrate:     getBool("DB_AUTO_MIGRATE", true, fail),
			SeedDemoData:    getBool("SEED_DEMO_DATA", env == "development", fail),
		},
		Hold: HoldConfig{
			TTL:                getDuration("HOLD_TTL", 5*time.Minute, fail),
			MaxSeatsPerBooking: getInt("MAX_SEATS_PER_BOOKING", 10, fail),
			BookingCutoff:      getDuration("BOOKING_CUTOFF", 10*time.Minute, fail),
			SweepInterval:      getDuration("HOLD_SWEEP_INTERVAL", 30*time.Second, fail),
			SweepBatch:         getInt("HOLD_SWEEP_BATCH", 500, fail),
		},
		Pay: PaymentConfig{
			Provider:            getEnv("PAYMENT_PROVIDER", ProviderMock),
			WebhookSecret:       getEnv("PAYMENT_WEBHOOK_SECRET", ""),
			PaymentWindow:       getDuration("PAYMENT_WINDOW", 10*time.Minute, fail),
			RefundCutoff:        getDuration("REFUND_CUTOFF", 2*time.Hour, fail),
			EnableTestEndpoints: getBool("PAYMENT_TEST_ENDPOINTS", env == "development", fail),
		},
		Log: LogConfig{
			Level:  getEnv("LOG_LEVEL", "info"),
			Format: getEnv("LOG_FORMAT", "json"),
		},
	}

	if cfg.DB.DSN == "" {
		fail("DATABASE_URL is required")
	}
	if cfg.DB.MinConns > cfg.DB.MaxConns {
		fail("DB_MIN_CONNS (%d) must not exceed DB_MAX_CONNS (%d)", cfg.DB.MinConns, cfg.DB.MaxConns)
	}
	if cfg.Hold.TTL < time.Minute {
		fail("HOLD_TTL must be at least 1m, got %s", cfg.Hold.TTL)
	}
	if cfg.Pay.PaymentWindow < cfg.Hold.TTL {
		// The checkout window extends the hold; a shorter one would let seats
		// lapse mid-payment.
		fail("PAYMENT_WINDOW (%s) must be at least HOLD_TTL (%s)", cfg.Pay.PaymentWindow, cfg.Hold.TTL)
	}
	if cfg.Pay.Provider != ProviderMock {
		fail("unsupported PAYMENT_PROVIDER %q (supported: %s)", cfg.Pay.Provider, ProviderMock)
	}

	// Production must not run with demo affordances or an unsigned webhook path.
	if env == "production" {
		if cfg.Pay.WebhookSecret == "" {
			fail("PAYMENT_WEBHOOK_SECRET is required in production")
		}
		if cfg.Pay.EnableTestEndpoints {
			fail("PAYMENT_TEST_ENDPOINTS must be false in production")
		}
		if cfg.DB.SeedDemoData {
			fail("SEED_DEMO_DATA must be false in production")
		}
		if len(cfg.HTTP.CORSOrigins) == 1 && cfg.HTTP.CORSOrigins[0] == "*" {
			fail("CORS_ORIGINS must list explicit origins in production")
		}
	}
	if cfg.Pay.WebhookSecret == "" {
		cfg.Pay.WebhookSecret = "dev-webhook-secret"
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// IsProduction reports whether the service runs in production mode.
func (c *Config) IsProduction() bool { return c.Env == "production" }

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func getDuration(key string, fallback time.Duration, fail func(string, ...any)) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		fail("%s must be a duration such as 30s or 5m, got %q", key, raw)
		return fallback
	}
	return d
}

func getInt(key string, fallback int, fail func(string, ...any)) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		fail("%s must be an integer, got %q", key, raw)
		return fallback
	}
	return v
}

func getFloat(key string, fallback float64, fail func(string, ...any)) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		fail("%s must be a number, got %q", key, raw)
		return fallback
	}
	return v
}

func getBool(key string, fallback bool, fail func(string, ...any)) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		fail("%s must be true or false, got %q", key, raw)
		return fallback
	}
	return v
}

func getList(key string, fallback []string) []string {
	raw := os.Getenv(key)
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
