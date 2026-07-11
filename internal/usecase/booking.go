package usecase

import (
	"context"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// BookingUsecase orchestrates the booking workflow.
// It depends only on the domain layer (repository interface).
// No HTTP, Redis, or other infrastructure knowledge here.
type BookingUsecase struct {
	repo domain.BookingRepository
}

// NewBookingUsecase creates a new booking use case.
// The repository can be any implementation (memory, Redis, etc).
func NewBookingUsecase(repo domain.BookingRepository) *BookingUsecase {
	return &BookingUsecase{repo: repo}
}

// HoldSeat attempts to temporarily reserve a seat for a user.
// Returns a session ID and expiration time.
// If the seat is already booked, returns ErrSeatAlreadyBooked.
func (u *BookingUsecase) HoldSeat(ctx context.Context, movieID string, seatID string, userID string) (domain.Booking, error) {
	booking := domain.Booking{
		MovieID: movieID,
		SeatID:  seatID,
		UserID:  userID,
		Status:  "held",
	}

	return u.repo.Hold(ctx, booking)
}

// ListSeats retrieves all bookings (held or confirmed) for a given movie.
// Used to display seat availability on the frontend.
func (u *BookingUsecase) ListSeats(ctx context.Context, movieID string) ([]domain.Booking, error) {
	return u.repo.ListByMovie(ctx, movieID)
}

// ConfirmSession converts a held booking into a confirmed (permanent) one.
// Returns error if the session doesn't exist or user is not authorized.
func (u *BookingUsecase) ConfirmSession(ctx context.Context, sessionID string, userID string) (domain.Booking, error) {
	return u.repo.Confirm(ctx, sessionID, userID)
}

// ReleaseSession cancels a held booking, making the seat available again.
// Does nothing if the session is already confirmed.
func (u *BookingUsecase) ReleaseSession(ctx context.Context, sessionID string, userID string) error {
	return u.repo.Release(ctx, sessionID, userID)
}
