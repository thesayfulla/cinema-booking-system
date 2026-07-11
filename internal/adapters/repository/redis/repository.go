package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

const defaultHoldTTL = 2 * time.Minute

// Repository implements domain.BookingRepository using Redis.
// Held bookings automatically expire after 2 minutes.
//
// Key design:
//   - seat:{movieID}:{seatID}   → booking JSON (TTL = held, no TTL = confirmed)
//   - session:{sessionID}       → seat key (reverse lookup for quick access)
type Repository struct {
	rdb *goredis.Client
}

// NewRepository creates a new Redis-backed booking repository.
func NewRepository(rdb *goredis.Client) *Repository {
	return &Repository{rdb: rdb}
}

// Hold creates a temporary booking with automatic expiration.
func (r *Repository) Hold(ctx context.Context, booking domain.Booking) (domain.Booking, error) {
	sessionID := uuid.New().String()
	now := time.Now()
	seatKey := r.seatKey(booking.MovieID, booking.SeatID)
	sessionKey := r.sessionKey(sessionID)

	// Marshal booking to JSON
	booking.ID = sessionID
	booking.Status = "held"
	booking.ExpiresAt = now.Add(defaultHoldTTL)

	bookingJSON, _ := json.Marshal(booking)

	// Try to set seat key (NX = only if not exists)
	// This ensures exactly one user can book the seat
	result := r.rdb.SetArgs(ctx, seatKey, bookingJSON, goredis.SetArgs{
		Mode: "NX",
		TTL:  defaultHoldTTL,
	})

	if result.Val() != "OK" {
		return domain.Booking{}, domain.ErrSeatAlreadyBooked
	}

	// Store reverse lookup: session -> seat key (for quick Confirm/Release)
	r.rdb.Set(ctx, sessionKey, seatKey, defaultHoldTTL)

	return booking, nil
}

// ListByMovie returns all bookings for a given movie (held or confirmed).
func (r *Repository) ListByMovie(ctx context.Context, movieID string) ([]domain.Booking, error) {
	pattern := r.seatKey(movieID, "*")
	var bookings []domain.Booking

	// Scan all seat keys matching the pattern
	iter := r.rdb.Scan(ctx, 0, pattern, 0).Iterator()
	for iter.Next(ctx) {
		val, err := r.rdb.Get(ctx, iter.Val()).Result()
		if err != nil {
			continue
		}

		booking, err := r.parseBooking(val)
		if err != nil {
			continue
		}

		bookings = append(bookings, booking)
	}

	return bookings, nil
}

// Confirm converts a held booking into a permanent one.
// Removes TTL so the booking never expires.
func (r *Repository) Confirm(ctx context.Context, sessionID string, userID string) (domain.Booking, error) {
	sessionKey := r.sessionKey(sessionID)

	// Get seat key from session lookup
	seatKey, err := r.rdb.Get(ctx, sessionKey).Result()
	if err != nil {
		return domain.Booking{}, domain.ErrSessionNotFound
	}

	// Get current booking
	val, err := r.rdb.Get(ctx, seatKey).Result()
	if err != nil {
		return domain.Booking{}, domain.ErrSessionNotFound
	}

	booking, err := r.parseBooking(val)
	if err != nil {
		return domain.Booking{}, domain.ErrSessionNotFound
	}

	// Verify user authorization
	if booking.UserID != userID {
		return domain.Booking{}, domain.ErrUnauthorized
	}

	// Check if already confirmed (no TTL)
	ttl, err := r.rdb.TTL(ctx, seatKey).Result()
	if err == nil && ttl < 0 && ttl != -1 {
		// TTL is -2 (key doesn't exist) or -1 (key has no expiration)
		// If -1, it means it's already confirmed
		if ttl == -1 {
			return domain.Booking{}, domain.ErrAlreadyConfirmed
		}
	}

	// Update booking status
	booking.Status = "confirmed"
	booking.ExpiresAt = time.Time{}
	bookingJSON, _ := json.Marshal(booking)

	// Persist the keys (remove TTL)
	r.rdb.Persist(ctx, seatKey)
	r.rdb.Persist(ctx, sessionKey)

	// Update the booking value
	r.rdb.Set(ctx, seatKey, bookingJSON, 0)

	return booking, nil
}

// Release removes a held booking, making the seat available again.
func (r *Repository) Release(ctx context.Context, sessionID string, userID string) error {
	sessionKey := r.sessionKey(sessionID)

	// Get seat key from session lookup
	seatKey, err := r.rdb.Get(ctx, sessionKey).Result()
	if err != nil {
		return domain.ErrSessionNotFound
	}

	// Get booking to verify user and status
	val, err := r.rdb.Get(ctx, seatKey).Result()
	if err != nil {
		return domain.ErrSessionNotFound
	}

	booking, err := r.parseBooking(val)
	if err != nil {
		return domain.ErrSessionNotFound
	}

	// Verify user authorization
	if booking.UserID != userID {
		return domain.ErrUnauthorized
	}

	// Don't allow release of confirmed bookings
	if booking.Status == "confirmed" {
		return domain.ErrAlreadyConfirmed
	}

	// Delete both keys
	r.rdb.Del(ctx, seatKey, sessionKey)

	return nil
}

// Helper methods

func (r *Repository) seatKey(movieID, seatID string) string {
	return fmt.Sprintf("seat:%s:%s", movieID, seatID)
}

func (r *Repository) sessionKey(sessionID string) string {
	return fmt.Sprintf("session:%s", sessionID)
}

func (r *Repository) parseBooking(val string) (domain.Booking, error) {
	var booking domain.Booking
	err := json.Unmarshal([]byte(val), &booking)
	return booking, err
}
