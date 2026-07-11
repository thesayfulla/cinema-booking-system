package usecase

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/thesayfulla/cinema-booking-system/internal/adapters/repository/memory"
	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// TestConcurrentBooking_ExactlyOneWins verifies that exactly one user wins when many try to book the same seat.
// This is a critical test for the booking system's atomicity guarantees.
func TestConcurrentBooking_ExactlyOneWins(t *testing.T) {
	// Use in-memory repository for this test
	repo := memory.NewRepository()
	uc := NewBookingUsecase(repo)

	const numGoroutines = 100

	var (
		successes atomic.Int64
		failures  atomic.Int64
		wg        sync.WaitGroup
	)

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(userNum int) {
			defer wg.Done()
			_, err := uc.HoldSeat(context.Background(), "screen-1", "A1", uuid.New().String())
			if err == nil {
				successes.Add(1)
			} else {
				failures.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Errorf("expected exactly 1 success, got %d", got)
	}
	if got := failures.Load(); got != int64(numGoroutines-1) {
		t.Errorf("expected %d failures, got %d", numGoroutines-1, got)
	}
}

// TestBookingWorkflow tests the complete booking workflow
func TestBookingWorkflow(t *testing.T) {
	repo := memory.NewRepository()
	uc := NewBookingUsecase(repo)
	ctx := context.Background()

	userID := "user123"
	movieID := "inception"
	seatID := "A1"

	// Step 1: Hold seat
	held, err := uc.HoldSeat(ctx, movieID, seatID, userID)
	if err != nil {
		t.Fatalf("hold seat failed: %v", err)
	}
	if held.Status != "held" {
		t.Errorf("expected status 'held', got %s", held.Status)
	}

	// Step 2: Verify seat shows as booked
	bookings, _ := uc.ListSeats(ctx, movieID)
	if len(bookings) != 1 {
		t.Errorf("expected 1 booking, got %d", len(bookings))
	}

	// Step 3: Confirm session
	confirmed, err := uc.ConfirmSession(ctx, held.ID, userID)
	if err != nil {
		t.Fatalf("confirm session failed: %v", err)
	}
	if confirmed.Status != "confirmed" {
		t.Errorf("expected status 'confirmed', got %s", confirmed.Status)
	}

	// Step 4: Release should fail for confirmed booking
	err = uc.ReleaseSession(ctx, held.ID, userID)
	if !errors.Is(err, domain.ErrAlreadyConfirmed) {
		t.Errorf("expected ErrAlreadyConfirmed, got %v", err)
	}
}
