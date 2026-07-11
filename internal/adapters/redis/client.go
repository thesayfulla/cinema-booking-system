package redis

import (
	"context"

	goredis "github.com/redis/go-redis/v9"
	"github.com/thesayfulla/cinema-booking-system/internal/logger"
)

// NewClient creates and connects to a Redis client.
// Returns an error if the connection fails.
func NewClient(addr string, logger *logger.Logger) (*goredis.Client, error) {
	rdb := goredis.NewClient(&goredis.Options{Addr: addr})

	// Test the connection
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		logger.Error("redis ping failed: %v", err)
		return nil, err
	}

	logger.Info("connected to redis at %s", addr)
	return rdb, nil
}
