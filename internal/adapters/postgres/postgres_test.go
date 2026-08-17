package postgres_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/adapters/postgres"
	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// These tests exercise the real concurrency guarantees, which only exist in
// Postgres: the partial unique index, transactional rollback of a partially
// claimed booking, and the expiry sweep. They are skipped unless
// TEST_DATABASE_URL points at a disposable database.
//
//	docker compose up -d postgres
//	TEST_DATABASE_URL="postgres://cinema:cinema@localhost:5432/cinema_test?sslmode=disable" go test ./internal/adapters/postgres/...

func testDB(t *testing.T) *postgres.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}

	ctx := context.Background()
	db, err := postgres.Connect(ctx, postgres.Config{
		DSN:            dsn,
		MaxConns:       30,
		MinConns:       2,
		ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)

	if _, err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Seed(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Each test starts from a clean booking ledger; the catalog is left alone.
	if _, err := db.Pool().Exec(ctx, `TRUNCATE payment_events, payments, booking_seats, bookings CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return db
}

// fixture returns a future showtime and its seats.
func fixture(t *testing.T, db *postgres.DB) (domain.Showtime, []domain.Seat) {
	t.Helper()
	ctx := context.Background()
	catalog := postgres.NewCatalogRepository(db)

	showtimes, err := catalog.ListShowtimes(ctx, "", time.Now())
	if err != nil {
		t.Fatalf("list showtimes: %v", err)
	}
	if len(showtimes) == 0 {
		// The seeded showtimes are anchored to today, so late in the day they
		// may all be in the past.
		t.Skip("no upcoming showtimes in the seeded catalog")
	}
	showtime := showtimes[0]

	availability, err := catalog.SeatMap(ctx, showtime.ID, "")
	if err != nil {
		t.Fatalf("seat map: %v", err)
	}
	seats := make([]domain.Seat, 0, len(availability))
	for _, a := range availability {
		seats = append(seats, a.Seat)
	}
	if len(seats) < 5 {
		t.Fatalf("expected at least 5 seats, got %d", len(seats))
	}
	return showtime, seats
}

func newBooking(showtime domain.Showtime, userID string, seats ...domain.Seat) domain.NewBooking {
	return domain.NewBooking{
		ShowtimeID:     showtime.ID,
		UserID:         userID,
		Seats:          seats,
		Currency:       showtime.Currency,
		BasePriceCents: showtime.BasePriceCents,
		HoldTTL:        5 * time.Minute,
	}
}

func TestHoldClaimsSeats(t *testing.T) {
	db := testDB(t)
	showtime, seats := fixture(t, db)
	repo := postgres.NewBookingRepository(db)
	ctx := context.Background()

	booking, err := repo.Hold(ctx, newBooking(showtime, "user-1", seats[0], seats[1]))
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}

	if booking.Status != domain.BookingHeld {
		t.Errorf("status = %q, want %q", booking.Status, domain.BookingHeld)
	}
	if booking.Reference == "" {
		t.Error("expected a booking reference")
	}
	want := seats[0].PriceCents(showtime.BasePriceCents) + seats[1].PriceCents(showtime.BasePriceCents)
	if booking.TotalAmountCents != want {
		t.Errorf("total = %d, want %d", booking.TotalAmountCents, want)
	}

	// The seat map must agree.
	availability, err := postgres.NewCatalogRepository(db).SeatMap(ctx, showtime.ID, "user-1")
	if err != nil {
		t.Fatalf("seat map: %v", err)
	}
	held := 0
	for _, a := range availability {
		if a.Status == domain.SeatStatusHeld {
			held++
			if !a.HeldByUser {
				t.Errorf("seat %s should be flagged as the caller's own", a.Seat.ID)
			}
		}
	}
	if held != 2 {
		t.Errorf("held seats = %d, want 2", held)
	}
}

// The load-bearing test: many goroutines fight for one seat, exactly one wins.
func TestConcurrentHoldsOnOneSeatProduceOneWinner(t *testing.T) {
	db := testDB(t)
	showtime, seats := fixture(t, db)
	repo := postgres.NewBookingRepository(db)

	const contenders = 30
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		wins      int
		conflicts int
		others    []error
	)

	start := make(chan struct{})
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start

			_, err := repo.Hold(context.Background(), newBooking(showtime, "user", seats[0]))

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, domain.ErrSeatUnavailable):
				conflicts++
			default:
				others = append(others, err)
			}
		}(i)
	}

	close(start)
	wg.Wait()

	if len(others) > 0 {
		t.Fatalf("unexpected errors: %v", others)
	}
	if wins != 1 {
		t.Errorf("winners = %d, want exactly 1", wins)
	}
	if conflicts != contenders-1 {
		t.Errorf("conflicts = %d, want %d", conflicts, contenders-1)
	}
}

// Overlapping multi-seat requests must not deadlock or half-claim.
func TestConcurrentOverlappingMultiSeatHolds(t *testing.T) {
	db := testDB(t)
	showtime, seats := fixture(t, db)
	repo := postgres.NewBookingRepository(db)

	// Two groups that overlap on the middle seat, requested in opposite orders.
	groups := [][]domain.Seat{
		{seats[0], seats[1], seats[2]},
		{seats[2], seats[1], seats[0]},
		{seats[1], seats[2], seats[3]},
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
		errs []error
	)

	start := make(chan struct{})
	for i, group := range groups {
		for attempt := 0; attempt < 5; attempt++ {
			wg.Add(1)
			go func(group []domain.Seat, n int) {
				defer wg.Done()
				<-start

				_, err := repo.Hold(context.Background(), newBooking(showtime, "user", group...))

				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					wins++
				case errors.Is(err, domain.ErrSeatUnavailable):
				default:
					errs = append(errs, err)
				}
			}(group, i)
		}
	}

	close(start)
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("unexpected errors (deadlocks should be retried internally): %v", errs)
	}
	if wins != 1 {
		// Every group overlaps every other group on at least one seat.
		t.Errorf("winners = %d, want exactly 1", wins)
	}

	availability, err := postgres.NewCatalogRepository(db).SeatMap(context.Background(), showtime.ID, "")
	if err != nil {
		t.Fatalf("seat map: %v", err)
	}
	taken := 0
	for _, a := range availability {
		if a.Status != domain.SeatStatusAvailable {
			taken++
		}
	}
	if taken != 3 {
		t.Errorf("claimed seats = %d, want 3 (one whole group)", taken)
	}
}

// A hold that hits a taken seat must roll back the seats it already claimed.
func TestHoldRollsBackPartialClaim(t *testing.T) {
	db := testDB(t)
	showtime, seats := fixture(t, db)
	repo := postgres.NewBookingRepository(db)
	ctx := context.Background()

	if _, err := repo.Hold(ctx, newBooking(showtime, "user-1", seats[2])); err != nil {
		t.Fatalf("first hold: %v", err)
	}

	if _, err := repo.Hold(ctx, newBooking(showtime, "user-2", seats[0], seats[1], seats[2])); !errors.Is(err, domain.ErrSeatUnavailable) {
		t.Fatalf("err = %v, want ErrSeatUnavailable", err)
	}

	// Seats 0 and 1 must be free for someone else.
	if _, err := repo.Hold(ctx, newBooking(showtime, "user-3", seats[0], seats[1])); err != nil {
		t.Fatalf("seats stayed claimed after a rolled-back hold: %v", err)
	}
}

func TestExpiredHoldFreesSeat(t *testing.T) {
	db := testDB(t)
	showtime, seats := fixture(t, db)
	repo := postgres.NewBookingRepository(db)
	ctx := context.Background()

	req := newBooking(showtime, "user-1", seats[0])
	req.HoldTTL = -time.Second // already lapsed

	first, err := repo.Hold(ctx, req)
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}

	// The seat map reports it as free straight away, before any sweep.
	availability, err := postgres.NewCatalogRepository(db).SeatMap(ctx, showtime.ID, "")
	if err != nil {
		t.Fatalf("seat map: %v", err)
	}
	for _, a := range availability {
		if a.Seat.ID == seats[0].ID && a.Status != domain.SeatStatusAvailable {
			t.Errorf("lapsed hold still shows as %q", a.Status)
		}
	}

	// And another user can claim it.
	if _, err := repo.Hold(ctx, newBooking(showtime, "user-2", seats[0])); err != nil {
		t.Fatalf("seat not reclaimable after the hold lapsed: %v", err)
	}

	stale, err := repo.GetByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stale.Status != domain.BookingExpired {
		t.Errorf("status = %q, want %q", stale.Status, domain.BookingExpired)
	}
}

func TestExpireDueHoldsSweepsLapsedBookings(t *testing.T) {
	db := testDB(t)
	showtime, seats := fixture(t, db)
	repo := postgres.NewBookingRepository(db)
	ctx := context.Background()

	req := newBooking(showtime, "user-1", seats[0], seats[1])
	req.HoldTTL = -time.Second
	booking, err := repo.Hold(ctx, req)
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}

	released, err := repo.ExpireDueHolds(ctx, 100)
	if err != nil {
		t.Fatalf("ExpireDueHolds: %v", err)
	}
	if released != 2 {
		t.Errorf("released = %d, want 2", released)
	}

	swept, err := repo.GetByID(ctx, booking.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if swept.Status != domain.BookingExpired {
		t.Errorf("status = %q, want %q", swept.Status, domain.BookingExpired)
	}
	if swept.HoldExpiresAt != nil {
		t.Error("an expired booking must not keep a hold expiry")
	}
}

func TestIdempotentHoldReturnsSameBooking(t *testing.T) {
	db := testDB(t)
	showtime, seats := fixture(t, db)
	repo := postgres.NewBookingRepository(db)
	ctx := context.Background()

	req := newBooking(showtime, "user-1", seats[0])
	req.IdempotencyKey = "same-request"

	first, err := repo.Hold(ctx, req)
	if err != nil {
		t.Fatalf("first hold: %v", err)
	}
	second, err := repo.Hold(ctx, req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if first.ID != second.ID {
		t.Errorf("replay created a second booking: %s vs %s", first.ID, second.ID)
	}
}

func TestConfirmAndCancelLifecycle(t *testing.T) {
	db := testDB(t)
	showtime, seats := fixture(t, db)
	repo := postgres.NewBookingRepository(db)
	ctx := context.Background()

	booking, err := repo.Hold(ctx, newBooking(showtime, "user-1", seats[0]))
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}

	confirmed, err := repo.Confirm(ctx, booking.ID)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if confirmed.Status != domain.BookingConfirmed || confirmed.ConfirmedAt == nil {
		t.Fatalf("unexpected confirmed booking: %+v", confirmed)
	}
	if confirmed.HoldExpiresAt != nil {
		t.Error("a confirmed booking must not keep a hold expiry")
	}

	// Confirming twice must not succeed twice.
	if _, err := repo.Confirm(ctx, booking.ID); !errors.Is(err, domain.ErrBookingState) {
		t.Errorf("second confirm err = %v, want ErrBookingState", err)
	}

	// A confirmed seat is not reclaimable by anyone else.
	if _, err := repo.Hold(ctx, newBooking(showtime, "user-2", seats[0])); !errors.Is(err, domain.ErrSeatUnavailable) {
		t.Errorf("err = %v, want ErrSeatUnavailable", err)
	}

	if _, err := repo.Cancel(ctx, booking.ID, domain.BookingCancelled); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := repo.Hold(ctx, newBooking(showtime, "user-2", seats[0])); err != nil {
		t.Fatalf("seat not released after cancel: %v", err)
	}
}

func TestPaymentLifecycle(t *testing.T) {
	db := testDB(t)
	showtime, seats := fixture(t, db)
	bookings := postgres.NewBookingRepository(db)
	payments := postgres.NewPaymentRepository(db)
	ctx := context.Background()

	booking, err := bookings.Hold(ctx, newBooking(showtime, "user-1", seats[0]))
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}

	created, err := payments.Create(ctx, domain.NewPayment{
		BookingID:      booking.ID,
		Provider:       "mock",
		ProviderRef:    "mock_pi_1",
		Status:         domain.PaymentPending,
		AmountCents:    booking.TotalAmountCents,
		Currency:       booking.Currency,
		IdempotencyKey: "booking:" + booking.ID,
		Metadata:       map[string]any{"reference": booking.Reference},
	})
	if err != nil {
		t.Fatalf("Create payment: %v", err)
	}

	// A replay of the same checkout returns the same payment.
	replay, err := payments.Create(ctx, domain.NewPayment{
		BookingID:      booking.ID,
		Provider:       "mock",
		ProviderRef:    "mock_pi_2",
		Status:         domain.PaymentPending,
		AmountCents:    booking.TotalAmountCents,
		Currency:       booking.Currency,
		IdempotencyKey: "booking:" + booking.ID,
	})
	if err != nil {
		t.Fatalf("replayed Create: %v", err)
	}
	if replay.ID != created.ID {
		t.Errorf("replay created a second payment: %s vs %s", replay.ID, created.ID)
	}

	// A second, differently keyed open payment is refused.
	_, err = payments.Create(ctx, domain.NewPayment{
		BookingID:      booking.ID,
		Provider:       "mock",
		ProviderRef:    "mock_pi_3",
		Status:         domain.PaymentPending,
		AmountCents:    booking.TotalAmountCents,
		Currency:       booking.Currency,
		IdempotencyKey: "another-key",
	})
	if !errors.Is(err, domain.ErrPaymentInProgress) {
		t.Errorf("err = %v, want ErrPaymentInProgress", err)
	}

	event := domain.PaymentEvent{
		Provider:    "mock",
		EventID:     "evt-1",
		Type:        domain.EventPaymentSucceeded,
		ProviderRef: "mock_pi_1",
		Payload:     map[string]any{"ok": true},
	}
	fresh, err := payments.RecordEvent(ctx, event)
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if !fresh {
		t.Error("first delivery should be reported as new")
	}
	fresh, err = payments.RecordEvent(ctx, event)
	if err != nil {
		t.Fatalf("RecordEvent replay: %v", err)
	}
	if fresh {
		t.Error("a redelivered event must be reported as a duplicate")
	}

	succeeded, err := payments.Update(ctx, created.ID, domain.PaymentUpdate{Status: domain.PaymentSucceeded})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if succeeded.Status != domain.PaymentSucceeded {
		t.Errorf("status = %q, want %q", succeeded.Status, domain.PaymentSucceeded)
	}

	// A late "failed" callback must not undo a captured charge.
	if _, err := payments.Update(ctx, created.ID, domain.PaymentUpdate{Status: domain.PaymentFailed}); !errors.Is(err, domain.ErrPaymentState) {
		t.Errorf("err = %v, want ErrPaymentState", err)
	}

	// Refunding a captured charge is allowed.
	if _, err := payments.Update(ctx, created.ID, domain.PaymentUpdate{Status: domain.PaymentRefunded}); err != nil {
		t.Errorf("refund update: %v", err)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	db := testDB(t)
	showtime, seats := fixture(t, db)
	repo := postgres.NewBookingRepository(db)
	ctx := context.Background()

	sentinel := errors.New("boom")
	err := db.WithTx(ctx, func(ctx context.Context) error {
		if _, err := repo.Hold(ctx, newBooking(showtime, "user-1", seats[0])); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the sentinel", err)
	}

	// Nothing from the rolled-back transaction may survive.
	if _, err := repo.Hold(ctx, newBooking(showtime, "user-2", seats[0])); err != nil {
		t.Fatalf("seat still claimed after rollback: %v", err)
	}
}
