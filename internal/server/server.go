package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/logger"
)

// Server wraps http.Server with additional lifecycle management.
type Server struct {
	server *http.Server
	logger *logger.Logger
}

// NewServer creates a new HTTP server.
func NewServer(addr string, handler http.Handler, logger *logger.Logger) *Server {
	return &Server{
		server: &http.Server{
			Addr:         addr,
			Handler:      handler,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
		logger: logger,
	}
}

// Start starts the HTTP server (blocking).
// Call in a goroutine if you need to run other code concurrently.
func (s *Server) Start() error {
	s.logger.Info("server starting on %s", s.server.Addr)
	err := s.server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() error {
	s.logger.Info("shutting down server")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

// Addr returns the server's listening address.
func (s *Server) Addr() string {
	return fmt.Sprintf("http://%s", s.server.Addr)
}
