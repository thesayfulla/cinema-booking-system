package usecase_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// The fakes below stand in for the Postgres adapters so the use case rules can
// be tested without a database. They deliberately keep the same guarantees the
// real repository provides — notably, one active claim per seat.

type fakeCatalog struct {
	showtime domain.Showtime
	seats    map[string]domain.Seat
	getErr   error
}

func newFakeCatalog(startsIn time.Duration, seatIDs ...string) *fakeCatalog {
	c := &fakeCatalog{
		showtime: domain.Showtime{
			ID:             "11111111-1111-4111-8111-111111111111",
			MovieID:        "22222222-2222-4222-8222-222222222222",
			HallID:         "33333333-3333-4333-8333-333333333333",
			StartsAt:       time.Now().Add(startsIn),
			BasePriceCents: 1000,
			Currency:       "USD",
			Status:         domain.ShowtimeScheduled,
		},
		seats: map[string]domain.Seat{},
	}
	for i, id := range seatIDs {
		c.seats[id] = domain.Seat{
			ID:         id,
			HallID:     c.showtime.HallID,
			RowLabel:   "A",
			SeatNumber: i + 1,
			SeatClass:  domain.SeatClassStandard,
		}
	}
	return c
}

func (c *fakeCatalog) ListMovies(context.Context) ([]domain.Movie, error) { return nil, nil }

func (c *fakeCatalog) GetMovie(context.Context, string) (domain.Movie, error) {
	return domain.Movie{ID: c.showtime.MovieID}, nil
}

func (c *fakeCatalog) ListShowtimes(context.Context, string, time.Time) ([]domain.Showtime, error) {
	return []domain.Showtime{c.showtime}, nil
}

func (c *fakeCatalog) GetShowtime(_ context.Context, id string) (domain.Showtime, error) {
	if c.getErr != nil {
		return domain.Showtime{}, c.getErr
	}
	if id != c.showtime.ID {
		return domain.Showtime{}, domain.ErrShowtimeNotFound
	}
	return c.showtime, nil
}

func (c *fakeCatalog) SeatMap(context.Context, string, string) ([]domain.SeatAvailability, error) {
	return nil, nil
}

func (c *fakeCatalog) SeatsByIDs(_ context.Context, hallID string, ids []string) ([]domain.Seat, error) {
	out := make([]domain.Seat, 0, len(ids))
	for _, id := range ids {
		seat, ok := c.seats[id]
		if !ok || seat.HallID != hallID {
			return nil, domain.ErrSeatNotFound
		}
		out = append(out, seat)
	}
	return out, nil
}

// fakeBookings mimics the Postgres booking repository, including its atomicity:
// a hold either claims every seat or none.
type fakeBookings struct {
	mu       sync.Mutex
	bookings map[string]*domain.Booking
	// claims maps showtime+seat to the booking actively holding it.
	claims  map[string]string
	nextID  int
	holdErr error
}

func newFakeBookings() *fakeBookings {
	return &fakeBookings{
		bookings: map[string]*domain.Booking{},
		claims:   map[string]string{},
	}
}

func claimKey(showtimeID, seatID string) string { return showtimeID + "/" + seatID }

func (f *fakeBookings) Hold(_ context.Context, req domain.NewBooking) (domain.Booking, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.holdErr != nil {
		return domain.Booking{}, f.holdErr
	}

	if req.IdempotencyKey != "" {
		for _, b := range f.bookings {
			if b.UserID == req.UserID && b.IdempotencyKey == req.IdempotencyKey {
				return *b, nil
			}
		}
	}

	f.releaseLapsedLocked()

	for _, seat := range req.Seats {
		if _, taken := f.claims[claimKey(req.ShowtimeID, seat.ID)]; taken {
			return domain.Booking{}, domain.ErrSeatUnavailable
		}
	}

	f.nextID++
	id := "booking-" + itoa(f.nextID)
	expires := time.Now().Add(req.HoldTTL)

	booking := &domain.Booking{
		ID:             id,
		Reference:      "CB-" + itoa(f.nextID),
		ShowtimeID:     req.ShowtimeID,
		UserID:         req.UserID,
		Status:         domain.BookingHeld,
		Currency:       req.Currency,
		HoldExpiresAt:  &expires,
		IdempotencyKey: req.IdempotencyKey,
		CreatedAt:      time.Now(),
	}
	for _, seat := range req.Seats {
		price := seat.PriceCents(req.BasePriceCents)
		booking.TotalAmountCents += price
		booking.Seats = append(booking.Seats, domain.BookedSeat{
			SeatID: seat.ID, RowLabel: seat.RowLabel, SeatNumber: seat.SeatNumber,
			SeatClass: seat.SeatClass, PriceCents: price, Active: true,
		})
		f.claims[claimKey(req.ShowtimeID, seat.ID)] = id
	}

	f.bookings[id] = booking
	return *booking, nil
}

func (f *fakeBookings) releaseLapsedLocked() {
	now := time.Now()
	for id, b := range f.bookings {
		if b.HoldLapsed(now) {
			b.Status = domain.BookingExpired
			b.HoldExpiresAt = nil
			f.releaseClaimsLocked(id)
		}
	}
}

func (f *fakeBookings) releaseClaimsLocked(bookingID string) {
	b := f.bookings[bookingID]
	for i := range b.Seats {
		b.Seats[i].Active = false
	}
	for key, owner := range f.claims {
		if owner == bookingID {
			delete(f.claims, key)
		}
	}
}

func (f *fakeBookings) GetByID(_ context.Context, id string) (domain.Booking, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bookings[id]
	if !ok {
		return domain.Booking{}, domain.ErrBookingNotFound
	}
	return *b, nil
}

func (f *fakeBookings) GetByIdempotencyKey(_ context.Context, userID, key string) (domain.Booking, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.bookings {
		if b.UserID == userID && b.IdempotencyKey == key && key != "" {
			return *b, nil
		}
	}
	return domain.Booking{}, domain.ErrBookingNotFound
}

func (f *fakeBookings) ListByUser(_ context.Context, userID string, _ int) ([]domain.Booking, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Booking
	for _, b := range f.bookings {
		if b.UserID == userID {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (f *fakeBookings) LockForUpdate(ctx context.Context, id string) (domain.Booking, error) {
	return f.GetByID(ctx, id)
}

func (f *fakeBookings) Confirm(_ context.Context, id string) (domain.Booking, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bookings[id]
	if !ok || b.Status != domain.BookingHeld {
		return domain.Booking{}, domain.ErrBookingState
	}
	now := time.Now()
	b.Status = domain.BookingConfirmed
	b.ConfirmedAt = &now
	b.HoldExpiresAt = nil
	return *b, nil
}

func (f *fakeBookings) Cancel(_ context.Context, id, status string) (domain.Booking, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bookings[id]
	if !ok || !b.IsActive() {
		return domain.Booking{}, domain.ErrBookingState
	}
	now := time.Now()
	b.Status = status
	b.CancelledAt = &now
	b.HoldExpiresAt = nil
	f.releaseClaimsLocked(id)
	return *b, nil
}

func (f *fakeBookings) ExtendHold(_ context.Context, id string, until time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bookings[id]
	if !ok || b.Status != domain.BookingHeld {
		return domain.ErrBookingState
	}
	if b.HoldExpiresAt == nil || until.After(*b.HoldExpiresAt) {
		b.HoldExpiresAt = &until
	}
	return nil
}

func (f *fakeBookings) ExpireDueHolds(_ context.Context, _ int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	before := len(f.claims)
	f.releaseLapsedLocked()
	return before - len(f.claims), nil
}

// expire forces a booking's hold to have lapsed, without waiting.
func (f *fakeBookings) expire(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	past := time.Now().Add(-time.Minute)
	f.bookings[id].HoldExpiresAt = &past
}

type fakePayments struct {
	mu       sync.Mutex
	payments map[string]*domain.Payment
	events   map[string]bool
	nextID   int
}

func newFakePayments() *fakePayments {
	return &fakePayments{payments: map[string]*domain.Payment{}, events: map[string]bool{}}
}

func (f *fakePayments) Create(_ context.Context, p domain.NewPayment) (domain.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, existing := range f.payments {
		if existing.IdempotencyKey == p.IdempotencyKey {
			return *existing, nil
		}
		if existing.BookingID == p.BookingID && existing.IsOpen() {
			return domain.Payment{}, domain.ErrPaymentInProgress
		}
	}

	f.nextID++
	payment := &domain.Payment{
		ID:             "payment-" + itoa(f.nextID),
		BookingID:      p.BookingID,
		Provider:       p.Provider,
		ProviderRef:    p.ProviderRef,
		Status:         p.Status,
		AmountCents:    p.AmountCents,
		Currency:       p.Currency,
		IdempotencyKey: p.IdempotencyKey,
	}
	f.payments[payment.ID] = payment
	return *payment, nil
}

func (f *fakePayments) GetByID(_ context.Context, id string) (domain.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.payments[id]
	if !ok {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	return *p, nil
}

func (f *fakePayments) GetByProviderRef(_ context.Context, provider, ref string) (domain.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.payments {
		if p.Provider == provider && p.ProviderRef == ref {
			return *p, nil
		}
	}
	return domain.Payment{}, domain.ErrPaymentNotFound
}

func (f *fakePayments) GetOpenByBooking(_ context.Context, bookingID string) (domain.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.payments {
		if p.BookingID == bookingID && p.IsOpen() {
			return *p, nil
		}
	}
	return domain.Payment{}, domain.ErrPaymentNotFound
}

func (f *fakePayments) GetSucceededByBooking(_ context.Context, bookingID string) (domain.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.payments {
		if p.BookingID == bookingID && p.Status == domain.PaymentSucceeded {
			return *p, nil
		}
	}
	return domain.Payment{}, domain.ErrPaymentNotFound
}

func (f *fakePayments) ListByBooking(_ context.Context, bookingID string) ([]domain.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Payment
	for _, p := range f.payments {
		if p.BookingID == bookingID {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (f *fakePayments) Update(_ context.Context, id string, u domain.PaymentUpdate) (domain.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.payments[id]
	if !ok {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	terminalOK := p.IsOpen() || (p.Status == domain.PaymentSucceeded && u.Status == domain.PaymentRefunded)
	if !terminalOK {
		return domain.Payment{}, domain.ErrPaymentState
	}
	p.Status = u.Status
	p.FailureReason = u.FailureReason
	return *p, nil
}

func (f *fakePayments) RecordEvent(_ context.Context, e domain.PaymentEvent) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := e.Provider + "/" + e.EventID
	if f.events[key] {
		return false, nil
	}
	f.events[key] = true
	return true, nil
}

// noopTx runs the callback without a real transaction.
type noopTx struct{}

func (noopTx) WithTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

// stubProvider is a payment provider whose behaviour each test can dictate.
type stubProvider struct {
	mu         sync.Mutex
	refunds    int
	cancels    int
	createErr  error
	refundErr  error
	parseErr   error
	nextEvent  domain.PaymentEvent
	nextRef    int
	lastIntent domain.PaymentIntentRequest
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) CreateIntent(_ context.Context, req domain.PaymentIntentRequest) (domain.PaymentIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return domain.PaymentIntent{}, s.createErr
	}
	s.lastIntent = req
	s.nextRef++
	return domain.PaymentIntent{
		ProviderRef:  "stub_ref_" + itoa(s.nextRef),
		Status:       domain.PaymentPending,
		ClientSecret: "secret",
	}, nil
}

func (s *stubProvider) Refund(_ context.Context, _ string, _ int64) (domain.RefundResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refundErr != nil {
		return domain.RefundResult{}, s.refundErr
	}
	s.refunds++
	return domain.RefundResult{ProviderRef: "stub_refund", Status: domain.PaymentRefunded}, nil
}

func (s *stubProvider) Cancel(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels++
	return nil
}

// ParseWebhook returns whatever event the test staged, so webhook handling can
// be exercised without a signature scheme.
func (s *stubProvider) ParseWebhook(_ context.Context, _ map[string]string, _ []byte) (domain.PaymentEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.parseErr != nil {
		return domain.PaymentEvent{}, s.parseErr
	}
	if s.nextEvent.EventID == "" {
		return domain.PaymentEvent{}, errors.New("no event staged")
	}
	return s.nextEvent, nil
}

// stage sets the event ParseWebhook will return.
func (s *stubProvider) stage(e domain.PaymentEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.Provider = s.Name()
	s.nextEvent = e
}

func (s *stubProvider) refundCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refunds
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
