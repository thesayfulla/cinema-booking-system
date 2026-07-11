package domain

import (
	"context"
	"errors"
	"time"
)

// Booking represents a seat reservation in the cinema system.
// It can be in two states: "held" (temporary) or "confirmed" (permanent).
type Booking struct {
	ID        string    // Unique session identifier
	MovieID   string    // Reference to movie
	SeatID    string    // Reference to seat within movie
	UserID    string    // User who made the booking
	Status    string    // "held" or "confirmed"
	ExpiresAt time.Time // Expiration time for held bookings
}

// BookingRepository defines the interface for storing and retrieving bookings.
// Implementations should handle both temporary (held) and permanent (confirmed) bookings.
type BookingRepository interface {
	// Hold creates a temporary booking that expires after a time.
	Hold(ctx context.Context, booking Booking) (Booking, error)

	// ListByMovie returns all bookings (held or confirmed) for a given movie.
	ListByMovie(ctx context.Context, movieID string) ([]Booking, error)

	// Confirm converts a held booking into a permanent one.
	// Returns error if the booking doesn't exist or is already confirmed.
	Confirm(ctx context.Context, sessionID string, userID string) (Booking, error)

	// Release removes a held booking.
	// Returns error if the booking doesn't exist.
	Release(ctx context.Context, sessionID string, userID string) error
}

// Domain-level errors
var (
	ErrSeatAlreadyBooked   = errors.New("seat is already booked")
	ErrSessionNotFound     = errors.New("session not found")
	ErrUnauthorized        = errors.New("user is not authorized for this session")
	ErrSessionExpired      = errors.New("session has expired")
	ErrInvalidInput        = errors.New("invalid input provided")
	ErrAlreadyConfirmed    = errors.New("session is already confirmed")
)
