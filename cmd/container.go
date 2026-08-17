package main

import (
	"context"
	"fmt"
	"log/slog"

	httpadapter "github.com/thesayfulla/cinema-booking-system/internal/adapters/http"
	"github.com/thesayfulla/cinema-booking-system/internal/adapters/payment/mock"
	"github.com/thesayfulla/cinema-booking-system/internal/adapters/postgres"
	"github.com/thesayfulla/cinema-booking-system/internal/config"
	"github.com/thesayfulla/cinema-booking-system/internal/domain"
	"github.com/thesayfulla/cinema-booking-system/internal/metrics"
	"github.com/thesayfulla/cinema-booking-system/internal/server"
	"github.com/thesayfulla/cinema-booking-system/internal/usecase"
	"github.com/thesayfulla/cinema-booking-system/internal/worker"
)

// Container holds the wired application. This is the one place that knows
// which concrete adapters back the domain's interfaces; every other package
// depends on the interfaces alone.
type Container struct {
	Config  *config.Config
	Log     *slog.Logger
	DB      *postgres.DB
	Server  *server.Server
	Expirer *worker.HoldExpirer
}

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

// NewContainer builds every dependency: configuration, database, repositories,
// use cases, handlers, and the HTTP server.
func NewContainer(ctx context.Context, cfg *config.Config, log *slog.Logger) (*Container, error) {
	db, err := postgres.Connect(ctx, postgres.Config{
		DSN:             cfg.DB.DSN,
		MaxConns:        cfg.DB.MaxConns,
		MinConns:        cfg.DB.MinConns,
		MaxConnLifetime: cfg.DB.MaxConnLifetime,
		MaxConnIdleTime: cfg.DB.MaxConnIdleTime,
		ConnectTimeout:  cfg.DB.ConnectTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}

	if cfg.DB.AutoMigrate {
		applied, err := db.Migrate(ctx)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("run migrations: %w", err)
		}
		if len(applied) > 0 {
			log.Info("applied database migrations", "migrations", applied)
		}
	}
	if cfg.DB.SeedDemoData {
		if err := db.Seed(ctx); err != nil {
			db.Close()
			return nil, err
		}
		log.Info("seeded demo catalog data")
	}

	// Repositories (adapters) satisfying the domain's ports.
	catalogRepo := postgres.NewCatalogRepository(db)
	bookingRepo := postgres.NewBookingRepository(db)
	paymentRepo := postgres.NewPaymentRepository(db)

	// Payment provider. Adding a real gateway means another case here.
	var (
		provider     domain.PaymentProvider
		mockProvider *mock.Provider
	)
	switch cfg.Pay.Provider {
	case config.ProviderMock:
		mockProvider = mock.New(cfg.Pay.WebhookSecret)
		provider = mockProvider
		log.Warn("using the mock payment provider; no real money moves")
	default:
		db.Close()
		return nil, fmt.Errorf("unsupported payment provider %q", cfg.Pay.Provider)
	}

	// Use cases.
	catalogUC := usecase.NewCatalog(catalogRepo)
	bookingUC := usecase.NewBooking(bookingRepo, catalogRepo, usecase.BookingPolicy{
		HoldTTL:            cfg.Hold.TTL,
		MaxSeatsPerBooking: cfg.Hold.MaxSeatsPerBooking,
		BookingCutoff:      cfg.Hold.BookingCutoff,
	})
	paymentUC := usecase.NewPayment(paymentRepo, bookingRepo, provider, db, usecase.PaymentPolicy{
		PaymentWindow: cfg.Pay.PaymentWindow,
		RefundCutoff:  cfg.Pay.RefundCutoff,
	}, log)

	// Transport.
	collector := metrics.New()
	router := httpadapter.NewRouter(httpadapter.RouterDeps{
		Config:  cfg,
		Log:     log,
		Metrics: collector,
		Catalog: httpadapter.NewCatalogHandler(catalogUC, log),
		Booking: httpadapter.NewBookingHandler(bookingUC, paymentUC, collector, log),
		Payment: httpadapter.NewPaymentHandler(paymentUC, collector, log, mockProvider),
		Health:  db.Ping,
		Version: version,
	})

	return &Container{
		Config:  cfg,
		Log:     log,
		DB:      db,
		Server:  server.New(cfg.HTTP, router, log),
		Expirer: worker.NewHoldExpirer(bookingUC, cfg.Hold.SweepInterval, cfg.Hold.SweepBatch, log, collector.HoldsReleased),
	}, nil
}

// Close releases the container's resources.
func (c *Container) Close() {
	if c.DB != nil {
		c.DB.Close()
	}
}
