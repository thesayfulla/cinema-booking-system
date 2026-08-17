// Command api runs the cinema booking service.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/thesayfulla/cinema-booking-system/internal/config"
	"github.com/thesayfulla/cinema-booking-system/internal/logger"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(cfg.Log.Level, cfg.Log.Format)
	log.Info("starting cinema booking service", "version", version, "env", cfg.Env)

	// SIGINT and SIGTERM cancel this context, which unwinds the server and the
	// background workers together.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	container, err := NewContainer(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer container.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		container.Expirer.Run(ctx)
	}()

	serveErr := container.Server.Run(ctx)

	// Stop the signal handler before waiting, so a second Ctrl-C aborts a
	// shutdown that is taking too long instead of being swallowed.
	stop()
	wg.Wait()

	if serveErr != nil {
		return fmt.Errorf("http server: %w", serveErr)
	}
	log.Info("shutdown complete")
	return nil
}
