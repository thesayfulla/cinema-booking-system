package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// BookingPolicy captures the business rules that are worth tuning per
// deployment rather than hardcoding.
type BookingPolicy struct {
	// HoldTTL is how long a seat stays reserved before checkout.
	HoldTTL time.Duration
	// MaxSeatsPerBooking caps a single order, a basic anti-scalping measure.
	MaxSeatsPerBooking int
	// BookingCutoff stops sales this long before a screening starts.
	BookingCutoff time.Duration
}

// Booking orchestrates the reservation lifecycle. It owns the rules; the
// repository owns atomicity.
type Booking struct {
	bookings domain.BookingRepository
	catalog  domain.CatalogRepository
	policy   BookingPolicy
}

// NewBooking builds the booking use case.
func NewBooking(bookings domain.BookingRepository, catalog domain.CatalogRepository, policy BookingPolicy) *Booking {
	if policy.HoldTTL <= 0 {
		policy.HoldTTL = 5 * time.Minute
	}
	if policy.MaxSeatsPerBooking <= 0 {
		policy.MaxSeatsPerBooking = 10
	}
	return &Booking{bookings: bookings, catalog: catalog, policy: policy}
}

// HoldSeatsInput is a request to reserve seats.
type HoldSeatsInput struct {
	ShowtimeID string
	SeatIDs    []string
	UserID     string
	// IdempotencyKey makes a retried request return the original booking
	// instead of reserving a second set of seats.
	IdempotencyKey string
}

// HoldSeats reserves seats for a limited time.
//
// Validation happens here; the actual claim is a single atomic repository call,
// so the checks below are conveniences that produce good error messages — they
// are never what keeps two users off the same seat.
func (b *Booking) HoldSeats(ctx context.Context, in HoldSeatsInput) (domain.Booking, error) {
	if in.UserID == "" {
		return domain.Booking{}, domain.Invalid("user_id", "is required")
	}
	if len(in.SeatIDs) == 0 {
		return domain.Booking{}, domain.Invalid("seat_ids", "at least one seat is required")
	}
	if len(in.SeatIDs) > b.policy.MaxSeatsPerBooking {
		return domain.Booking{}, domain.Invalid("seat_ids",
			fmt.Sprintf("at most %d seats per booking", b.policy.MaxSeatsPerBooking))
	}
	if dup := firstDuplicate(in.SeatIDs); dup != "" {
		return domain.Booking{}, domain.Invalid("seat_ids", "seat "+dup+" is listed twice")
	}

	// An idempotent replay must not consume a second set of seats.
	if in.IdempotencyKey != "" {
		existing, err := b.bookings.GetByIdempotencyKey(ctx, in.UserID, in.IdempotencyKey)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, domain.ErrBookingNotFound) {
			return domain.Booking{}, err
		}
	}

	showtime, err := b.catalog.GetShowtime(ctx, in.ShowtimeID)
	if err != nil {
		return domain.Booking{}, err
	}
	if err := b.assertBookable(showtime); err != nil {
		return domain.Booking{}, err
	}

	seats, err := b.catalog.SeatsByIDs(ctx, showtime.HallID, in.SeatIDs)
	if err != nil {
		return domain.Booking{}, err
	}

	return b.bookings.Hold(ctx, domain.NewBooking{
		ShowtimeID:     showtime.ID,
		UserID:         in.UserID,
		Seats:          seats,
		Currency:       showtime.Currency,
		BasePriceCents: showtime.BasePriceCents,
		HoldTTL:        b.policy.HoldTTL,
		IdempotencyKey: in.IdempotencyKey,
	})
}

// Get returns a booking, enforcing that it belongs to the caller.
func (b *Booking) Get(ctx context.Context, bookingID, userID string) (domain.Booking, error) {
	booking, err := b.bookings.GetByID(ctx, bookingID)
	if err != nil {
		return domain.Booking{}, err
	}
	if booking.UserID != userID {
		// Deliberately reported as "not found": confirming that a booking id
		// exists would leak information to someone probing ids.
		return domain.Booking{}, domain.ErrBookingNotFound
	}

	// A held booking whose window lapsed is reported as expired even if the
	// sweeper has not rewritten the row yet, so clients see one consistent story.
	if booking.HoldLapsed(time.Now()) {
		booking.Status = domain.BookingExpired
	}
	return booking, nil
}

// ListForUser returns a user's booking history, newest first.
func (b *Booking) ListForUser(ctx context.Context, userID string, limit int) ([]domain.Booking, error) {
	if userID == "" {
		return nil, domain.Invalid("user_id", "is required")
	}

	bookings, err := b.bookings.ListByUser(ctx, userID, limit)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for i := range bookings {
		if bookings[i].HoldLapsed(now) {
			bookings[i].Status = domain.BookingExpired
		}
	}
	return bookings, nil
}

// Release cancels a held booking and frees its seats. Confirmed bookings are
// not released here: refunding a paid booking goes through the payment use
// case, which has to return the money as well as the seats.
func (b *Booking) Release(ctx context.Context, bookingID, userID string) error {
	booking, err := b.bookings.GetByID(ctx, bookingID)
	if err != nil {
		return err
	}
	if booking.UserID != userID {
		return domain.ErrBookingNotFound
	}
	if booking.Status != domain.BookingHeld {
		return domain.ErrBookingState
	}

	_, err = b.bookings.Cancel(ctx, bookingID, domain.BookingCancelled)
	return err
}

// ExpireHolds releases lapsed holds; the background sweeper drives it.
func (b *Booking) ExpireHolds(ctx context.Context, limit int) (int, error) {
	return b.bookings.ExpireDueHolds(ctx, limit)
}

// assertBookable rejects screenings that can no longer be sold.
func (b *Booking) assertBookable(s domain.Showtime) error {
	if s.Status == domain.ShowtimeCancelled {
		return domain.ErrShowtimeCanceled
	}
	if time.Now().Add(b.policy.BookingCutoff).After(s.StartsAt) {
		return domain.ErrShowtimeStarted
	}
	return nil
}

func firstDuplicate(ids []string) string {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			return id
		}
		seen[id] = struct{}{}
	}
	return ""
}
