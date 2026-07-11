package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

func TestHold_Success(t *testing.T) {
	repo := NewRepository()
	ctx := context.Background()

	booking, err := repo.Hold(ctx, domain.Booking{
		MovieID: "inception",
		SeatID:  "A1",
		UserID:  "user1",
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if booking.ID == "" {
		t.Error("expected session ID to be set")
	}
	if booking.Status != "held" {
		t.Errorf("expected status 'held', got %s", booking.Status)
	}
}

func TestHold_SeatAlreadyBooked(t *testing.T) {
	repo := NewRepository()
	ctx := context.Background()

	// First booking should succeed
	_, err := repo.Hold(ctx, domain.Booking{
		MovieID: "inception",
		SeatID:  "A1",
		UserID:  "user1",
	})
	if err != nil {
		t.Fatalf("first hold failed: %v", err)
	}

	// Second booking for same seat should fail
	_, err = repo.Hold(ctx, domain.Booking{
		MovieID: "inception",
		SeatID:  "A1",
		UserID:  "user2",
	})

	if !errors.Is(err, domain.ErrSeatAlreadyBooked) {
		t.Errorf("expected ErrSeatAlreadyBooked, got %v", err)
	}
}

func TestListByMovie(t *testing.T) {
	repo := NewRepository()
	ctx := context.Background()

	// Add some bookings
	repo.Hold(ctx, domain.Booking{MovieID: "inception", SeatID: "A1", UserID: "user1"})
	repo.Hold(ctx, domain.Booking{MovieID: "inception", SeatID: "A2", UserID: "user2"})
	repo.Hold(ctx, domain.Booking{MovieID: "dune", SeatID: "B1", UserID: "user3"})

	// List inception bookings
	bookings, err := repo.ListByMovie(ctx, "inception")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(bookings) != 2 {
		t.Errorf("expected 2 bookings for inception, got %d", len(bookings))
	}
}

func TestConfirm_Success(t *testing.T) {
	repo := NewRepository()
	ctx := context.Background()

	// Hold a seat first
	held, _ := repo.Hold(ctx, domain.Booking{
		MovieID: "inception",
		SeatID:  "A1",
		UserID:  "user1",
	})

	// Confirm it
	confirmed, err := repo.Confirm(ctx, held.ID, "user1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if confirmed.Status != "confirmed" {
		t.Errorf("expected status 'confirmed', got %s", confirmed.Status)
	}
}

func TestConfirm_Unauthorized(t *testing.T) {
	repo := NewRepository()
	ctx := context.Background()

	held, _ := repo.Hold(ctx, domain.Booking{
		MovieID: "inception",
		SeatID:  "A1",
		UserID:  "user1",
	})

	// Try to confirm with different user
	_, err := repo.Confirm(ctx, held.ID, "user2")

	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestRelease_Success(t *testing.T) {
	repo := NewRepository()
	ctx := context.Background()

	held, _ := repo.Hold(ctx, domain.Booking{
		MovieID: "inception",
		SeatID:  "A1",
		UserID:  "user1",
	})

	err := repo.Release(ctx, held.ID, "user1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify seat is now available
	_, err = repo.Hold(ctx, domain.Booking{
		MovieID: "inception",
		SeatID:  "A1",
		UserID:  "user2",
	})
	if err != nil {
		t.Errorf("expected seat to be available after release, got error: %v", err)
	}
}

func TestRelease_NotFound(t *testing.T) {
	repo := NewRepository()
	ctx := context.Background()

	err := repo.Release(ctx, "nonexistent", "user1")

	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}
