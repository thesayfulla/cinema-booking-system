package memory

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// Repository implements domain.BookingRepository using in-memory storage.
// Thread-safe using RWMutex.
// Held bookings expire after 2 minutes (in production, would use cleanup goroutine).
type Repository struct {
	mu       sync.RWMutex
	// Store bookings by session ID (primary key)
	bookings map[string]domain.Booking
	// Index seat bookings for quick lookup: movieID:seatID -> sessionID
	seatIndex map[string]string
}

// NewRepository creates a new in-memory booking repository.
func NewRepository() *Repository {
	return &Repository{
		bookings:  make(map[string]domain.Booking),
		seatIndex: make(map[string]string),
	}
}

// Hold creates a temporary booking that expires after 2 minutes.
func (r *Repository) Hold(ctx context.Context, booking domain.Booking) (domain.Booking, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if seat is already booked
	seatKey := booking.MovieID + ":" + booking.SeatID
	if _, exists := r.seatIndex[seatKey]; exists {
		return domain.Booking{}, domain.ErrSeatAlreadyBooked
	}

	// Create new booking with UUID and expiration
	sessionID := uuid.New().String()
	booking.ID = sessionID
	booking.Status = "held"
	booking.ExpiresAt = time.Now().Add(2 * time.Minute)

	r.bookings[sessionID] = booking
	r.seatIndex[seatKey] = sessionID

	return booking, nil
}

// ListByMovie returns all bookings for a given movie.
func (r *Repository) ListByMovie(ctx context.Context, movieID string) ([]domain.Booking, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []domain.Booking
	for _, b := range r.bookings {
		if b.MovieID == movieID {
			result = append(result, b)
		}
	}

	return result, nil
}

// Confirm converts a held booking into a permanent one.
func (r *Repository) Confirm(ctx context.Context, sessionID string, userID string) (domain.Booking, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	booking, exists := r.bookings[sessionID]
	if !exists {
		return domain.Booking{}, domain.ErrSessionNotFound
	}

	// Verify user authorization
	if booking.UserID != userID {
		return domain.Booking{}, domain.ErrUnauthorized
	}

	// Check if already confirmed
	if booking.Status == "confirmed" {
		return domain.Booking{}, domain.ErrAlreadyConfirmed
	}

	// Remove expiration and mark as confirmed
	booking.Status = "confirmed"
	booking.ExpiresAt = time.Time{}

	r.bookings[sessionID] = booking
	return booking, nil
}

// Release removes a held booking.
func (r *Repository) Release(ctx context.Context, sessionID string, userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	booking, exists := r.bookings[sessionID]
	if !exists {
		return domain.ErrSessionNotFound
	}

	// Verify user authorization
	if booking.UserID != userID {
		return domain.ErrUnauthorized
	}

	// Only allow release of held bookings
	if booking.Status == "confirmed" {
		return domain.ErrAlreadyConfirmed
	}

	// Remove booking
	delete(r.bookings, sessionID)

	// Also remove from seat index
	seatKey := booking.MovieID + ":" + booking.SeatID
	delete(r.seatIndex, seatKey)

	return nil
}
