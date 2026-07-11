package main

import (
	"context"
	"errors"

	"github.com/thesayfulla/cinema-booking-system/internal/adapters/http"
	redisAdapter "github.com/thesayfulla/cinema-booking-system/internal/adapters/redis"
	"github.com/thesayfulla/cinema-booking-system/internal/adapters/repository/memory"
	"github.com/thesayfulla/cinema-booking-system/internal/adapters/repository/redis"
	"github.com/thesayfulla/cinema-booking-system/internal/config"
	"github.com/thesayfulla/cinema-booking-system/internal/domain"
	"github.com/thesayfulla/cinema-booking-system/internal/logger"
	"github.com/thesayfulla/cinema-booking-system/internal/server"
	"github.com/thesayfulla/cinema-booking-system/internal/usecase"
)

// Container holds all application dependencies.
// This is the single place where all layers are wired together.
// It follows dependency injection principles: dependencies flow through constructors,
// not globals, making the app testable and modular.
type Container struct {
	Config *config.Config
	Logger *logger.Logger
	Server *server.Server
	Router *http.Router
}

// NewContainer bootstraps the entire application with proper dependency injection.
// It's responsible for:
// 1. Loading configuration
// 2. Initializing infrastructure (logger, database)
// 3. Creating use cases
// 4. Creating HTTP handlers
// 5. Wiring routes
func NewContainer(ctx context.Context) (*Container, error) {
	// Load configuration from environment
	cfg := config.NewConfig()

	// Initialize logger
	log := logger.NewLogger()

	// Initialize repository based on configuration
	// This is the only place where storage implementation is chosen
	var bookingRepo domain.BookingRepository

	switch cfg.StorageBackend {
	case "memory":
		log.Info("using in-memory repository")
		bookingRepo = memory.NewRepository()

	case "redis":
		log.Info("using redis repository")
		rdb, err := redisAdapter.NewClient(cfg.RedisAddr, log)
		if err != nil {
			log.Fatal("failed to connect to redis: %v", err)
		}
		bookingRepo = redis.NewRepository(rdb)

	default:
		return nil, errors.New("invalid storage backend: " + cfg.StorageBackend)
	}

	// Initialize use cases
	// Use cases depend only on domain interfaces (bookingRepo implements domain.BookingRepository)
	bookingUC := usecase.NewBookingUsecase(bookingRepo)

	// Create HTTP handlers
	// Handlers depend only on use cases
	bookingHandler := http.NewBookingHandler(bookingUC)
	moviesHandler := http.NewMoviesHandler()

	// Create router and register all routes
	router := http.NewRouter(bookingHandler, moviesHandler)

	// Create HTTP server
	httpServer := server.NewServer(":"+cfg.HTTPPort, router.Handler(), log)

	return &Container{
		Config: cfg,
		Logger: log,
		Server: httpServer,
		Router: router,
	}, nil
}
