// Package worker holds background jobs that keep the system tidy.
package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/usecase"
)

// HoldExpirer periodically releases seat holds whose window has passed.
//
// Correctness does not depend on it: reads treat a lapsed hold as free and a
// new hold reclaims lapsed seats in its own transaction. The sweeper keeps the
// data honest so bookings do not sit in 'held' forever and the seat-claim index
// stays small. Several replicas can run it at once — the batch query skips
// locked rows.
type HoldExpirer struct {
	bookings *usecase.Booking
	interval time.Duration
	batch    int
	log      *slog.Logger
	// onReleased reports how many seat claims a pass freed, for metrics.
	onReleased func(int)
}

// NewHoldExpirer builds the sweeper. onReleased may be nil.
func NewHoldExpirer(bookings *usecase.Booking, interval time.Duration, batch int, log *slog.Logger, onReleased func(int)) *HoldExpirer {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if batch <= 0 {
		batch = 500
	}
	return &HoldExpirer{bookings: bookings, interval: interval, batch: batch, log: log, onReleased: onReleased}
}

// Run sweeps until ctx is cancelled.
func (e *HoldExpirer) Run(ctx context.Context) {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	e.log.InfoContext(ctx, "hold expirer started", "interval", e.interval.String(), "batch", e.batch)

	for {
		select {
		case <-ctx.Done():
			e.log.InfoContext(ctx, "hold expirer stopped")
			return
		case <-ticker.C:
			e.sweep(ctx)
		}
	}
}

func (e *HoldExpirer) sweep(ctx context.Context) {
	// Bound each pass so a stuck database cannot wedge the sweeper forever.
	sweepCtx, cancel := context.WithTimeout(ctx, e.interval)
	defer cancel()

	released, err := e.bookings.ExpireHolds(sweepCtx, e.batch)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down
		}
		e.log.ErrorContext(ctx, "hold expiry sweep failed", "error", err)
		return
	}
	if released > 0 {
		if e.onReleased != nil {
			e.onReleased(released)
		}
		e.log.InfoContext(ctx, "released expired seat holds", "seats", released)
	}
}
