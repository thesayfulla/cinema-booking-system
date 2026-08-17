package domain

import (
	"context"
	"time"
)

// Booking lifecycle.
//
//	held ──confirm──> confirmed
//	  │                   │
//	  ├──release──> cancelled <──refund/cancel──┘
//	  └──ttl lapse──> expired
//
// Only held bookings expire; confirmed bookings hold their seats permanently.
const (
	BookingHeld      = "held"
	BookingConfirmed = "confirmed"
	BookingCancelled = "cancelled"
	BookingExpired   = "expired"
)

// Booking is a group of seats reserved for one user at one showtime.
type Booking struct {
	ID               string
	Reference        string
	ShowtimeID       string
	UserID           string
	Status           string
	TotalAmountCents int64
	Currency         string
	// HoldExpiresAt is set while Status is held and nil in terminal states.
	HoldExpiresAt  *time.Time
	IdempotencyKey string
	ConfirmedAt    *time.Time
	CancelledAt    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time

	Seats    []BookedSeat
	Showtime *Showtime
}

// BookedSeat is one seat claimed by a booking at the price charged for it.
type BookedSeat struct {
	SeatID     string
	RowLabel   string
	SeatNumber int
	SeatClass  string
	PriceCents int64
	Active     bool
}

// IsActive reports whether the booking currently holds its seats.
func (b Booking) IsActive() bool {
	return b.Status == BookingHeld || b.Status == BookingConfirmed
}

// HoldLapsed reports whether a held booking's hold window has passed.
func (b Booking) HoldLapsed(now time.Time) bool {
	return b.Status == BookingHeld && b.HoldExpiresAt != nil && !b.HoldExpiresAt.After(now)
}

// NewBooking describes a hold request handed to the repository.
type NewBooking struct {
	ShowtimeID     string
	UserID         string
	Seats          []Seat
	Currency       string
	BasePriceCents int64
	HoldTTL        time.Duration
	IdempotencyKey string
}

// BookingRepository persists bookings and their seat claims.
//
// Hold must be atomic: either every requested seat is claimed or none is, and a
// seat claimed by another active booking must fail with ErrSeatUnavailable.
type BookingRepository interface {
	Hold(ctx context.Context, req NewBooking) (Booking, error)
	GetByID(ctx context.Context, bookingID string) (Booking, error)
	// GetByIdempotencyKey returns the booking a previous identical request
	// created, or ErrBookingNotFound.
	GetByIdempotencyKey(ctx context.Context, userID, key string) (Booking, error)
	ListByUser(ctx context.Context, userID string, limit int) ([]Booking, error)
	// LockForUpdate reads a booking inside the current transaction, blocking
	// concurrent writers until it commits.
	LockForUpdate(ctx context.Context, bookingID string) (Booking, error)
	Confirm(ctx context.Context, bookingID string) (Booking, error)
	// Cancel releases the booking's seats and marks it cancelled or expired.
	Cancel(ctx context.Context, bookingID string, status string) (Booking, error)
	// ExtendHold pushes a held booking's expiry out, used while a payment is
	// in flight so the seats survive the checkout round-trip.
	ExtendHold(ctx context.Context, bookingID string, until time.Time) error
	// ExpireDueHolds releases every hold whose window lapsed and returns how
	// many bookings it released.
	ExpireDueHolds(ctx context.Context, limit int) (int, error)
}

// TxManager runs fn inside a single database transaction. Repositories called
// with the context fn receives participate in that transaction.
type TxManager interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
}
