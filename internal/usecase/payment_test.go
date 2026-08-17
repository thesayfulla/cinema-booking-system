package usecase_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
	"github.com/thesayfulla/cinema-booking-system/internal/usecase"
)

type paymentFixture struct {
	payment  *usecase.Payment
	booking  *usecase.Booking
	bookings *fakeBookings
	payments *fakePayments
	provider *stubProvider
	catalog  *fakeCatalog
}

func newPaymentFixture(t *testing.T) paymentFixture {
	t.Helper()

	catalog := newFakeCatalog(24*time.Hour, seatA1, seatA2, seatA3)
	bookings := newFakeBookings()
	payments := newFakePayments()
	provider := &stubProvider{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	return paymentFixture{
		payment: usecase.NewPayment(payments, bookings, provider, noopTx{}, usecase.PaymentPolicy{
			PaymentWindow: 10 * time.Minute,
			RefundCutoff:  2 * time.Hour,
		}, log),
		booking:  usecase.NewBooking(bookings, catalog, usecase.BookingPolicy{HoldTTL: 5 * time.Minute}),
		bookings: bookings,
		payments: payments,
		provider: provider,
		catalog:  catalog,
	}
}

func (f paymentFixture) hold(t *testing.T, userID string, seatIDs ...string) domain.Booking {
	t.Helper()
	booking, err := f.booking.HoldSeats(context.Background(), usecase.HoldSeatsInput{
		ShowtimeID: f.catalog.showtime.ID, SeatIDs: seatIDs, UserID: userID,
	})
	if err != nil {
		t.Fatalf("HoldSeats: %v", err)
	}
	return booking
}

func TestStartCheckoutCreatesPaymentAndExtendsHold(t *testing.T) {
	f := newPaymentFixture(t)
	booking := f.hold(t, "user-1", seatA1)
	originalExpiry := *booking.HoldExpiresAt

	checkout, err := f.payment.StartCheckout(context.Background(), booking.ID, "user-1")
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}

	if checkout.Payment.Status != domain.PaymentPending {
		t.Errorf("status = %q, want %q", checkout.Payment.Status, domain.PaymentPending)
	}
	if checkout.Payment.AmountCents != booking.TotalAmountCents {
		t.Errorf("amount = %d, want %d", checkout.Payment.AmountCents, booking.TotalAmountCents)
	}
	if checkout.Intent.ClientSecret == "" {
		t.Error("expected the provider's client secret to be passed through")
	}

	// The hold must outlive the checkout round-trip.
	refreshed, err := f.booking.Get(context.Background(), booking.ID, "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !refreshed.HoldExpiresAt.After(originalExpiry) {
		t.Error("expected the hold to be extended for the payment window")
	}
}

// Starting checkout twice must not charge the customer twice.
func TestStartCheckoutReturnsExistingPayment(t *testing.T) {
	f := newPaymentFixture(t)
	booking := f.hold(t, "user-1", seatA1)
	ctx := context.Background()

	first, err := f.payment.StartCheckout(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("first checkout: %v", err)
	}
	second, err := f.payment.StartCheckout(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("second checkout: %v", err)
	}

	if first.Payment.ID != second.Payment.ID {
		t.Errorf("a second payment was created: %s vs %s", first.Payment.ID, second.Payment.ID)
	}
}

func TestStartCheckoutRejectsOtherUsersAndBadStates(t *testing.T) {
	ctx := context.Background()

	t.Run("not the owner", func(t *testing.T) {
		f := newPaymentFixture(t)
		booking := f.hold(t, "owner", seatA1)
		if _, err := f.payment.StartCheckout(ctx, booking.ID, "intruder"); !errors.Is(err, domain.ErrBookingNotFound) {
			t.Fatalf("err = %v, want ErrBookingNotFound", err)
		}
	})

	t.Run("hold already lapsed", func(t *testing.T) {
		f := newPaymentFixture(t)
		booking := f.hold(t, "user-1", seatA1)
		f.bookings.expire(booking.ID)

		if _, err := f.payment.StartCheckout(ctx, booking.ID, "user-1"); !errors.Is(err, domain.ErrBookingExpired) {
			t.Fatalf("err = %v, want ErrBookingExpired", err)
		}
	})

	t.Run("already cancelled", func(t *testing.T) {
		f := newPaymentFixture(t)
		booking := f.hold(t, "user-1", seatA1)
		if err := f.booking.Release(ctx, booking.ID, "user-1"); err != nil {
			t.Fatalf("Release: %v", err)
		}

		if _, err := f.payment.StartCheckout(ctx, booking.ID, "user-1"); !errors.Is(err, domain.ErrBookingState) {
			t.Fatalf("err = %v, want ErrBookingState", err)
		}
	})
}

func TestWebhookSuccessConfirmsBooking(t *testing.T) {
	f := newPaymentFixture(t)
	ctx := context.Background()
	booking := f.hold(t, "user-1", seatA1, seatA2)

	checkout, err := f.payment.StartCheckout(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}

	f.provider.stage(domain.PaymentEvent{
		EventID:     "evt-1",
		Type:        domain.EventPaymentSucceeded,
		ProviderRef: checkout.Payment.ProviderRef,
	})
	if err := f.payment.HandleWebhook(ctx, nil, nil); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}

	confirmed, err := f.booking.Get(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if confirmed.Status != domain.BookingConfirmed {
		t.Errorf("booking status = %q, want %q", confirmed.Status, domain.BookingConfirmed)
	}
	if confirmed.HoldExpiresAt != nil {
		t.Error("a confirmed booking must not keep a hold expiry")
	}

	payment, err := f.payment.GetPayment(ctx, checkout.Payment.ID, "user-1")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if payment.Status != domain.PaymentSucceeded {
		t.Errorf("payment status = %q, want %q", payment.Status, domain.PaymentSucceeded)
	}
}

// Providers retry deliveries; the second one must change nothing.
func TestWebhookIsIdempotent(t *testing.T) {
	f := newPaymentFixture(t)
	ctx := context.Background()
	booking := f.hold(t, "user-1", seatA1)

	checkout, err := f.payment.StartCheckout(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}

	event := domain.PaymentEvent{
		EventID:     "evt-duplicate",
		Type:        domain.EventPaymentSucceeded,
		ProviderRef: checkout.Payment.ProviderRef,
	}
	f.provider.stage(event)

	for i := 0; i < 3; i++ {
		if err := f.payment.HandleWebhook(ctx, nil, nil); err != nil {
			t.Fatalf("delivery %d: %v", i+1, err)
		}
	}

	confirmed, err := f.booking.Get(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if confirmed.Status != domain.BookingConfirmed {
		t.Errorf("status = %q, want %q", confirmed.Status, domain.BookingConfirmed)
	}
	if f.provider.refundCount() != 0 {
		t.Errorf("refunds = %d, want 0", f.provider.refundCount())
	}
}

// A failed charge leaves the seats held so the customer can try another card.
func TestWebhookFailureKeepsHold(t *testing.T) {
	f := newPaymentFixture(t)
	ctx := context.Background()
	booking := f.hold(t, "user-1", seatA1)

	checkout, err := f.payment.StartCheckout(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}

	f.provider.stage(domain.PaymentEvent{
		EventID:     "evt-failed",
		Type:        domain.EventPaymentFailed,
		ProviderRef: checkout.Payment.ProviderRef,
		Payload:     map[string]any{"reason": "card declined"},
	})
	if err := f.payment.HandleWebhook(ctx, nil, nil); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}

	after, err := f.booking.Get(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.Status != domain.BookingHeld {
		t.Errorf("booking status = %q, want %q", after.Status, domain.BookingHeld)
	}

	payment, err := f.payment.GetPayment(ctx, checkout.Payment.ID, "user-1")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if payment.Status != domain.PaymentFailed {
		t.Errorf("payment status = %q, want %q", payment.Status, domain.PaymentFailed)
	}
	if payment.FailureReason != "card declined" {
		t.Errorf("failure reason = %q, want the provider's reason", payment.FailureReason)
	}
}

// A declined card must not be the end of the road: the hold survives, so the
// customer has to be able to start a fresh charge on the same booking.
func TestStartCheckoutAfterFailedPaymentCreatesNewPayment(t *testing.T) {
	f := newPaymentFixture(t)
	ctx := context.Background()
	booking := f.hold(t, "user-1", seatA1)

	first, err := f.payment.StartCheckout(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("first checkout: %v", err)
	}

	f.provider.stage(domain.PaymentEvent{
		EventID:     "evt-declined",
		Type:        domain.EventPaymentFailed,
		ProviderRef: first.Payment.ProviderRef,
		Payload:     map[string]any{"reason": "card declined"},
	})
	if err := f.payment.HandleWebhook(ctx, nil, nil); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}

	second, err := f.payment.StartCheckout(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("retry checkout: %v", err)
	}
	if second.Payment.ID == first.Payment.ID {
		t.Fatalf("retry returned the failed payment %s instead of a new one", first.Payment.ID)
	}
	if second.Payment.Status != domain.PaymentPending {
		t.Errorf("retry payment status = %q, want %q", second.Payment.Status, domain.PaymentPending)
	}

	// And that new charge must still be able to confirm the booking.
	f.provider.stage(domain.PaymentEvent{
		EventID:     "evt-succeeded",
		Type:        domain.EventPaymentSucceeded,
		ProviderRef: second.Payment.ProviderRef,
	})
	if err := f.payment.HandleWebhook(ctx, nil, nil); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}

	after, err := f.booking.Get(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.Status != domain.BookingConfirmed {
		t.Errorf("booking status = %q, want %q", after.Status, domain.BookingConfirmed)
	}
}

// If the money lands after the booking has already lost its seats, the charge
// must be refunded rather than silently kept.
func TestWebhookSuccessRefundsWhenBookingIsGone(t *testing.T) {
	f := newPaymentFixture(t)
	ctx := context.Background()
	booking := f.hold(t, "user-1", seatA1)

	checkout, err := f.payment.StartCheckout(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}
	if _, err := f.bookings.Cancel(ctx, booking.ID, domain.BookingCancelled); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	f.provider.stage(domain.PaymentEvent{
		EventID:     "evt-late",
		Type:        domain.EventPaymentSucceeded,
		ProviderRef: checkout.Payment.ProviderRef,
	})
	if err := f.payment.HandleWebhook(ctx, nil, nil); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}

	if f.provider.refundCount() != 1 {
		t.Fatalf("refunds = %d, want 1", f.provider.refundCount())
	}
	payment, err := f.payments.GetByID(ctx, checkout.Payment.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if payment.Status != domain.PaymentRefunded {
		t.Errorf("payment status = %q, want %q", payment.Status, domain.PaymentRefunded)
	}
}

func TestCancelConfirmedBookingRefundsAndFreesSeats(t *testing.T) {
	f := newPaymentFixture(t)
	ctx := context.Background()
	booking := f.hold(t, "user-1", seatA1)

	checkout, err := f.payment.StartCheckout(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}
	f.provider.stage(domain.PaymentEvent{
		EventID:     "evt-ok",
		Type:        domain.EventPaymentSucceeded,
		ProviderRef: checkout.Payment.ProviderRef,
	})
	if err := f.payment.HandleWebhook(ctx, nil, nil); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}

	cancelled, err := f.payment.CancelBooking(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("CancelBooking: %v", err)
	}
	if cancelled.Status != domain.BookingCancelled {
		t.Errorf("status = %q, want %q", cancelled.Status, domain.BookingCancelled)
	}
	if f.provider.refundCount() != 1 {
		t.Errorf("refunds = %d, want 1", f.provider.refundCount())
	}

	// The seat is available again.
	if _, err := f.booking.HoldSeats(ctx, usecase.HoldSeatsInput{
		ShowtimeID: f.catalog.showtime.ID, SeatIDs: []string{seatA1}, UserID: "user-2",
	}); err != nil {
		t.Fatalf("seat not released after refund: %v", err)
	}
}

// A refund that the provider rejects must leave the booking intact rather than
// releasing seats the customer already paid for.
func TestCancelKeepsBookingWhenRefundFails(t *testing.T) {
	f := newPaymentFixture(t)
	ctx := context.Background()
	booking := f.hold(t, "user-1", seatA1)

	checkout, err := f.payment.StartCheckout(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}
	f.provider.stage(domain.PaymentEvent{
		EventID:     "evt-ok",
		Type:        domain.EventPaymentSucceeded,
		ProviderRef: checkout.Payment.ProviderRef,
	})
	if err := f.payment.HandleWebhook(ctx, nil, nil); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}

	f.provider.refundErr = errors.New("gateway timeout")

	if _, err := f.payment.CancelBooking(ctx, booking.ID, "user-1"); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}

	after, err := f.booking.Get(ctx, booking.ID, "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.Status != domain.BookingConfirmed {
		t.Errorf("status = %q, want the booking to stay confirmed", after.Status)
	}
}
