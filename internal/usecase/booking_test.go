package usecase_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
	"github.com/thesayfulla/cinema-booking-system/internal/usecase"
)

const (
	seatA1 = "aaaa1111-0000-4000-8000-000000000001"
	seatA2 = "aaaa1111-0000-4000-8000-000000000002"
	seatA3 = "aaaa1111-0000-4000-8000-000000000003"
)

func newBookingUC(t *testing.T, policy usecase.BookingPolicy) (*usecase.Booking, *fakeBookings, *fakeCatalog) {
	t.Helper()
	catalog := newFakeCatalog(24*time.Hour, seatA1, seatA2, seatA3)
	bookings := newFakeBookings()
	return usecase.NewBooking(bookings, catalog, policy), bookings, catalog
}

func TestHoldSeatsReservesRequestedSeats(t *testing.T) {
	uc, _, catalog := newBookingUC(t, usecase.BookingPolicy{HoldTTL: 5 * time.Minute})

	booking, err := uc.HoldSeats(context.Background(), usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID,
		SeatIDs:    []string{seatA1, seatA2},
		UserID:     "user-1",
	})
	if err != nil {
		t.Fatalf("HoldSeats: %v", err)
	}

	if booking.Status != domain.BookingHeld {
		t.Errorf("status = %q, want %q", booking.Status, domain.BookingHeld)
	}
	if len(booking.Seats) != 2 {
		t.Errorf("seats = %d, want 2", len(booking.Seats))
	}
	// Two standard seats at the showtime's base price.
	if want := int64(2000); booking.TotalAmountCents != want {
		t.Errorf("total = %d, want %d", booking.TotalAmountCents, want)
	}
	if booking.HoldExpiresAt == nil || !booking.HoldExpiresAt.After(time.Now()) {
		t.Error("expected a hold expiry in the future")
	}
}

func TestHoldSeatsRejectsSeatAlreadyHeld(t *testing.T) {
	uc, _, catalog := newBookingUC(t, usecase.BookingPolicy{HoldTTL: 5 * time.Minute})
	ctx := context.Background()

	if _, err := uc.HoldSeats(ctx, usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA1}, UserID: "user-1",
	}); err != nil {
		t.Fatalf("first hold: %v", err)
	}

	_, err := uc.HoldSeats(ctx, usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA1}, UserID: "user-2",
	})
	if !errors.Is(err, domain.ErrSeatUnavailable) {
		t.Fatalf("err = %v, want ErrSeatUnavailable", err)
	}
}

// A hold covering several seats must not leave a partial reservation behind
// when one of the seats is taken.
func TestHoldSeatsIsAllOrNothing(t *testing.T) {
	uc, repo, catalog := newBookingUC(t, usecase.BookingPolicy{HoldTTL: 5 * time.Minute})
	ctx := context.Background()

	if _, err := uc.HoldSeats(ctx, usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA2}, UserID: "user-1",
	}); err != nil {
		t.Fatalf("first hold: %v", err)
	}

	_, err := uc.HoldSeats(ctx, usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA1, seatA2, seatA3}, UserID: "user-2",
	})
	if !errors.Is(err, domain.ErrSeatUnavailable) {
		t.Fatalf("err = %v, want ErrSeatUnavailable", err)
	}

	// Seats A1 and A3 must be free for the next customer.
	if _, err := uc.HoldSeats(ctx, usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA1, seatA3}, UserID: "user-3",
	}); err != nil {
		t.Fatalf("seats were left claimed by the failed hold: %v", err)
	}

	if got := len(repo.claims); got != 3 {
		t.Errorf("active claims = %d, want 3", got)
	}
}

// Only one of many simultaneous requests for the same seat may win.
func TestHoldSeatsConcurrentRequestsProduceOneWinner(t *testing.T) {
	uc, _, catalog := newBookingUC(t, usecase.BookingPolicy{HoldTTL: 5 * time.Minute})

	const contenders = 25
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		wins      int
		conflicts int
	)

	start := make(chan struct{})
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start

			_, err := uc.HoldSeats(context.Background(), usecase.HoldSeatsInput{
				ShowtimeID: catalog.showtime.ID,
				SeatIDs:    []string{seatA1},
				UserID:     "user-" + itoa(n),
			})

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, domain.ErrSeatUnavailable):
				conflicts++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}

	close(start)
	wg.Wait()

	if wins != 1 {
		t.Errorf("winners = %d, want exactly 1", wins)
	}
	if conflicts != contenders-1 {
		t.Errorf("conflicts = %d, want %d", conflicts, contenders-1)
	}
}

func TestHoldSeatsIsIdempotent(t *testing.T) {
	uc, _, catalog := newBookingUC(t, usecase.BookingPolicy{HoldTTL: 5 * time.Minute})
	ctx := context.Background()

	in := usecase.HoldSeatsInput{
		ShowtimeID:     catalog.showtime.ID,
		SeatIDs:        []string{seatA1},
		UserID:         "user-1",
		IdempotencyKey: "checkout-42",
	}

	first, err := uc.HoldSeats(ctx, in)
	if err != nil {
		t.Fatalf("first hold: %v", err)
	}
	second, err := uc.HoldSeats(ctx, in)
	if err != nil {
		t.Fatalf("replayed hold: %v", err)
	}

	if first.ID != second.ID {
		t.Errorf("replay created a new booking: %s vs %s", first.ID, second.ID)
	}
}

func TestHoldSeatsValidation(t *testing.T) {
	uc, _, catalog := newBookingUC(t, usecase.BookingPolicy{HoldTTL: 5 * time.Minute, MaxSeatsPerBooking: 2})

	tests := []struct {
		name string
		in   usecase.HoldSeatsInput
		want error
	}{
		{
			name: "no user",
			in:   usecase.HoldSeatsInput{ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA1}},
			want: domain.ErrValidation,
		},
		{
			name: "no seats",
			in:   usecase.HoldSeatsInput{ShowtimeID: catalog.showtime.ID, UserID: "user-1"},
			want: domain.ErrValidation,
		},
		{
			name: "too many seats",
			in: usecase.HoldSeatsInput{ShowtimeID: catalog.showtime.ID, UserID: "user-1",
				SeatIDs: []string{seatA1, seatA2, seatA3}},
			want: domain.ErrValidation,
		},
		{
			name: "duplicate seat",
			in: usecase.HoldSeatsInput{ShowtimeID: catalog.showtime.ID, UserID: "user-1",
				SeatIDs: []string{seatA1, seatA1}},
			want: domain.ErrValidation,
		},
		{
			name: "unknown showtime",
			in:   usecase.HoldSeatsInput{ShowtimeID: "nope", UserID: "user-1", SeatIDs: []string{seatA1}},
			want: domain.ErrShowtimeNotFound,
		},
		{
			name: "seat from another hall",
			in: usecase.HoldSeatsInput{ShowtimeID: catalog.showtime.ID, UserID: "user-1",
				SeatIDs: []string{"ffff1111-0000-4000-8000-000000000009"}},
			want: domain.ErrSeatNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := uc.HoldSeats(context.Background(), tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// Sales stop shortly before the screening starts.
func TestHoldSeatsRejectsShowtimePastCutoff(t *testing.T) {
	catalog := newFakeCatalog(5*time.Minute, seatA1)
	uc := usecase.NewBooking(newFakeBookings(), catalog, usecase.BookingPolicy{
		HoldTTL:       5 * time.Minute,
		BookingCutoff: 10 * time.Minute,
	})

	_, err := uc.HoldSeats(context.Background(), usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA1}, UserID: "user-1",
	})
	if !errors.Is(err, domain.ErrShowtimeStarted) {
		t.Fatalf("err = %v, want ErrShowtimeStarted", err)
	}
}

func TestHoldSeatsRejectsCancelledShowtime(t *testing.T) {
	catalog := newFakeCatalog(24*time.Hour, seatA1)
	catalog.showtime.Status = domain.ShowtimeCancelled
	uc := usecase.NewBooking(newFakeBookings(), catalog, usecase.BookingPolicy{HoldTTL: 5 * time.Minute})

	_, err := uc.HoldSeats(context.Background(), usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA1}, UserID: "user-1",
	})
	if !errors.Is(err, domain.ErrShowtimeCanceled) {
		t.Fatalf("err = %v, want ErrShowtimeCanceled", err)
	}
}

// An expired hold frees its seat for the next customer.
func TestExpiredHoldReleasesSeat(t *testing.T) {
	uc, repo, catalog := newBookingUC(t, usecase.BookingPolicy{HoldTTL: 5 * time.Minute})
	ctx := context.Background()

	first, err := uc.HoldSeats(ctx, usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA1}, UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("first hold: %v", err)
	}

	repo.expire(first.ID)

	if _, err := uc.HoldSeats(ctx, usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA1}, UserID: "user-2",
	}); err != nil {
		t.Fatalf("seat was not released after the hold lapsed: %v", err)
	}

	// The lapsed booking reads back as expired even before the sweeper runs.
	stale, err := uc.Get(ctx, first.ID, "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stale.Status != domain.BookingExpired {
		t.Errorf("status = %q, want %q", stale.Status, domain.BookingExpired)
	}
}

func TestGetHidesOtherUsersBookings(t *testing.T) {
	uc, _, catalog := newBookingUC(t, usecase.BookingPolicy{HoldTTL: 5 * time.Minute})
	ctx := context.Background()

	booking, err := uc.HoldSeats(ctx, usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA1}, UserID: "owner",
	})
	if err != nil {
		t.Fatalf("HoldSeats: %v", err)
	}

	if _, err := uc.Get(ctx, booking.ID, "someone-else"); !errors.Is(err, domain.ErrBookingNotFound) {
		t.Fatalf("err = %v, want ErrBookingNotFound", err)
	}
}

func TestReleaseFreesSeatAndRejectsNonOwner(t *testing.T) {
	uc, _, catalog := newBookingUC(t, usecase.BookingPolicy{HoldTTL: 5 * time.Minute})
	ctx := context.Background()

	booking, err := uc.HoldSeats(ctx, usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA1}, UserID: "owner",
	})
	if err != nil {
		t.Fatalf("HoldSeats: %v", err)
	}

	if err := uc.Release(ctx, booking.ID, "intruder"); !errors.Is(err, domain.ErrBookingNotFound) {
		t.Fatalf("err = %v, want ErrBookingNotFound", err)
	}
	if err := uc.Release(ctx, booking.ID, "owner"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if _, err := uc.HoldSeats(ctx, usecase.HoldSeatsInput{
		ShowtimeID: catalog.showtime.ID, SeatIDs: []string{seatA1}, UserID: "user-2",
	}); err != nil {
		t.Fatalf("seat not free after release: %v", err)
	}
}
