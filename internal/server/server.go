// Package server runs the HTTP listener with a graceful shutdown.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/config"
)

// Server wraps http.Server with lifecycle management.
type Server struct {
	http            *http.Server
	log             *slog.Logger
	shutdownTimeout time.Duration
}

// New builds the HTTP server from configuration.
func New(cfg config.HTTPConfig, handler http.Handler, log *slog.Logger) *Server {
	return &Server{
		http: &http.Server{
			Addr:              ":" + cfg.Port,
			Handler:           handler,
			ReadTimeout:       cfg.ReadTimeout,
			ReadHeaderTimeout: cfg.ReadTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		},
		log:             log,
		shutdownTimeout: cfg.ShutdownTimeout,
	}
}

// Run serves until ctx is cancelled, then drains in-flight requests.
//
// Cancelling ctx (on SIGTERM, say) stops accepting new connections and gives
// running handlers up to the shutdown timeout to finish, so a deploy does not
// cut a checkout in half.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		s.log.Info("http server listening", "addr", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.log.Info("shutting down http server", "timeout", s.shutdownTimeout.String())

		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
		defer cancel()

		if err := s.http.Shutdown(shutdownCtx); err != nil {
			// Requests still running at the deadline are cut off here.
			s.log.Error("graceful shutdown timed out; forcing close", "error", err)
			return s.http.Close()
		}
		s.log.Info("http server stopped cleanly")
		return nil
	}
}
