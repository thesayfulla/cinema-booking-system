package usecase

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// PaymentPolicy holds the tunable parts of the checkout flow.
type PaymentPolicy struct {
	// PaymentWindow is how long the seats stay held once checkout starts. It
	// should comfortably exceed the slowest realistic gateway round-trip.
	PaymentWindow time.Duration
	// RefundCutoff blocks self-service refunds once a screening is this close.
	RefundCutoff time.Duration
}

// Payment drives checkout: it creates charges at the provider, reacts to the
// provider's callbacks, and keeps the booking's state in step with the money.
type Payment struct {
	payments domain.PaymentRepository
	bookings domain.BookingRepository
	provider domain.PaymentProvider
	tx       domain.TxManager
	policy   PaymentPolicy
	log      *slog.Logger
}

// NewPayment builds the payment use case.
func NewPayment(
	payments domain.PaymentRepository,
	bookings domain.BookingRepository,
	provider domain.PaymentProvider,
	tx domain.TxManager,
	policy PaymentPolicy,
	log *slog.Logger,
) *Payment {
	if policy.PaymentWindow <= 0 {
		policy.PaymentWindow = 10 * time.Minute
	}
	return &Payment{
		payments: payments,
		bookings: bookings,
		provider: provider,
		tx:       tx,
		policy:   policy,
		log:      log,
	}
}

// Checkout is the result of starting a payment.
type Checkout struct {
	Payment domain.Payment
	Intent  domain.PaymentIntent
}

// StartCheckout creates (or returns) the payment for a held booking.
//
// Calling it twice returns the same payment rather than charging twice: the
// open-payment index makes that a database guarantee, not a race the caller
// has to avoid.
func (p *Payment) StartCheckout(ctx context.Context, bookingID, userID string) (Checkout, error) {
	booking, err := p.bookings.GetByID(ctx, bookingID)
	if err != nil {
		return Checkout{}, err
	}
	if booking.UserID != userID {
		return Checkout{}, domain.ErrBookingNotFound
	}
	if booking.Status != domain.BookingHeld {
		return Checkout{}, domain.ErrBookingState
	}
	if booking.HoldLapsed(time.Now()) {
		return Checkout{}, domain.ErrBookingExpired
	}

	// An in-flight payment means the user already started checkout; hand back
	// the same intent so a refreshed page resumes instead of charging again.
	if existing, err := p.payments.GetOpenByBooking(ctx, booking.ID); err == nil {
		return Checkout{
			Payment: existing,
			Intent: domain.PaymentIntent{
				ProviderRef: existing.ProviderRef,
				Status:      existing.Status,
			},
		}, nil
	} else if !errors.Is(err, domain.ErrPaymentNotFound) {
		return Checkout{}, err
	}

	// Give the checkout room to finish before the sweeper can reclaim the seats.
	holdUntil := time.Now().Add(p.policy.PaymentWindow)
	if err := p.bookings.ExtendHold(ctx, booking.ID, holdUntil); err != nil {
		return Checkout{}, err
	}

	// One key per checkout attempt. Keying on the booking alone would make a
	// second attempt — after a card was declined, say — collide with the
	// settled payment and hand the customer back the failed charge with no way
	// to pay. Charging twice is prevented by the single-open-payment index
	// below, not by this key.
	idempotencyKey, err := newAttemptKey(booking.ID)
	if err != nil {
		return Checkout{}, err
	}

	intent, err := p.provider.CreateIntent(ctx, domain.PaymentIntentRequest{
		BookingID:      booking.ID,
		Reference:      booking.Reference,
		UserID:         booking.UserID,
		AmountCents:    booking.TotalAmountCents,
		Currency:       booking.Currency,
		Description:    "Cinema booking " + booking.Reference,
		IdempotencyKey: idempotencyKey,
		Metadata: map[string]string{
			"booking_id":        booking.ID,
			"booking_reference": booking.Reference,
			"showtime_id":       booking.ShowtimeID,
		},
	})
	if err != nil {
		if errors.Is(err, domain.ErrValidation) {
			return Checkout{}, err
		}
		return Checkout{}, fmt.Errorf("%w: %s", domain.ErrProviderUnavailable, err)
	}

	status := intent.Status
	if status == "" {
		status = domain.PaymentPending
	}

	payment, err := p.payments.Create(ctx, domain.NewPayment{
		BookingID:      booking.ID,
		Provider:       p.provider.Name(),
		ProviderRef:    intent.ProviderRef,
		Status:         status,
		AmountCents:    booking.TotalAmountCents,
		Currency:       booking.Currency,
		IdempotencyKey: idempotencyKey,
		Metadata:       map[string]any{"booking_reference": booking.Reference},
	})
	if errors.Is(err, domain.ErrPaymentInProgress) {
		// A concurrent checkout opened a payment between the lookup above and
		// this insert. Drop the intent we just created and resume theirs, so a
		// double-click cannot leave two charges outstanding.
		if cancelErr := p.provider.Cancel(ctx, intent.ProviderRef); cancelErr != nil {
			p.log.WarnContext(ctx, "could not cancel superseded payment intent",
				"booking_id", booking.ID, "provider_ref", intent.ProviderRef, "error", cancelErr)
		}
		existing, getErr := p.payments.GetOpenByBooking(ctx, booking.ID)
		if getErr != nil {
			return Checkout{}, err
		}
		return Checkout{
			Payment: existing,
			Intent: domain.PaymentIntent{
				ProviderRef: existing.ProviderRef,
				Status:      existing.Status,
			},
		}, nil
	}
	if err != nil {
		return Checkout{}, err
	}

	return Checkout{Payment: payment, Intent: intent}, nil
}

// newAttemptKey returns an idempotency key unique to one checkout attempt.
func newAttemptKey(bookingID string) (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate idempotency key: %w", err)
	}
	return "booking:" + bookingID + ":" + hex.EncodeToString(buf), nil
}

// GetPayment returns a payment, enforcing that its booking belongs to the caller.
func (p *Payment) GetPayment(ctx context.Context, paymentID, userID string) (domain.Payment, error) {
	payment, err := p.payments.GetByID(ctx, paymentID)
	if err != nil {
		return domain.Payment{}, err
	}
	booking, err := p.bookings.GetByID(ctx, payment.BookingID)
	if err != nil {
		return domain.Payment{}, err
	}
	if booking.UserID != userID {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	return payment, nil
}

// HandleWebhook processes a provider callback.
//
// Providers retry deliveries and may deliver out of order, so this is written
// to be safely repeatable: the event is recorded under a unique key first and a
// second delivery of the same event does nothing.
func (p *Payment) HandleWebhook(ctx context.Context, headers map[string]string, body []byte) error {
	event, err := p.provider.ParseWebhook(ctx, headers, body)
	if err != nil {
		return err
	}

	// refundRef is set when the money has to go back: the charge succeeded but
	// the booking is no longer holdable. The provider call happens after the
	// transaction commits, never while holding row locks.
	var refundRef string
	var refundAmount int64

	err = p.tx.WithTx(ctx, func(ctx context.Context) error {
		fresh, err := p.payments.RecordEvent(ctx, event)
		if err != nil {
			return err
		}
		if !fresh {
			p.log.InfoContext(ctx, "ignoring duplicate payment webhook",
				"provider", event.Provider, "event_id", event.EventID)
			return nil
		}

		payment, err := p.payments.GetByProviderRef(ctx, event.Provider, event.ProviderRef)
		if errors.Is(err, domain.ErrPaymentNotFound) {
			// A callback for a charge we never recorded. Keep the event row for
			// investigation and acknowledge, so the provider stops retrying.
			p.log.WarnContext(ctx, "payment webhook for unknown payment",
				"provider", event.Provider, "provider_ref", event.ProviderRef, "type", event.Type)
			return nil
		}
		if err != nil {
			return err
		}

		switch event.Type {
		case domain.EventPaymentSucceeded:
			refundRef, refundAmount, err = p.applySuccess(ctx, payment)
			return err

		case domain.EventPaymentFailed:
			// The booking keeps its hold: the user can retry payment until the
			// hold lapses on its own.
			return p.settle(ctx, payment, domain.PaymentFailed, stringOr(event.Payload["reason"], "payment failed"))

		case domain.EventPaymentCancelled:
			return p.settle(ctx, payment, domain.PaymentCancelled, "cancelled at provider")

		case domain.EventPaymentRefunded:
			return p.applyRefund(ctx, payment)

		default:
			p.log.WarnContext(ctx, "unhandled payment event type", "type", event.Type)
			return nil
		}
	})
	if err != nil {
		return err
	}

	if refundRef != "" {
		p.refundOrphanedCharge(ctx, refundRef, refundAmount, event.ProviderRef)
	}
	return nil
}

// applySuccess marks the charge captured and confirms the booking. If the
// booking is no longer holdable — its seats went to someone else while the
// gateway was working — it reports the charge for refund instead.
func (p *Payment) applySuccess(ctx context.Context, payment domain.Payment) (refundRef string, refundAmount int64, err error) {
	if payment.Status != domain.PaymentSucceeded {
		if _, err := p.payments.Update(ctx, payment.ID, domain.PaymentUpdate{Status: domain.PaymentSucceeded}); err != nil {
			return "", 0, err
		}
	}

	booking, err := p.bookings.LockForUpdate(ctx, payment.BookingID)
	if err != nil {
		return "", 0, err
	}

	switch booking.Status {
	case domain.BookingConfirmed:
		// An earlier delivery already confirmed it; nothing left to do.
		return "", 0, nil

	case domain.BookingHeld:
		// Confirm even if the hold window lapsed a moment ago: the seats are
		// still claimed by this booking and the customer has paid.
		if _, err := p.bookings.Confirm(ctx, booking.ID); err != nil {
			return "", 0, err
		}
		p.log.InfoContext(ctx, "booking confirmed by payment",
			"booking_id", booking.ID, "reference", booking.Reference, "payment_id", payment.ID)
		return "", 0, nil

	default:
		// cancelled or expired: we hold money for seats we cannot deliver.
		p.log.ErrorContext(ctx, "payment succeeded for a booking that lost its seats; refunding",
			"booking_id", booking.ID, "booking_status", booking.Status, "payment_id", payment.ID)
		return payment.ID, payment.AmountCents, nil
	}
}

// applyRefund records a refund and releases the booking's seats.
func (p *Payment) applyRefund(ctx context.Context, payment domain.Payment) error {
	if payment.Status != domain.PaymentRefunded {
		if _, err := p.payments.Update(ctx, payment.ID, domain.PaymentUpdate{Status: domain.PaymentRefunded}); err != nil &&
			!errors.Is(err, domain.ErrPaymentState) {
			return err
		}
	}

	booking, err := p.bookings.LockForUpdate(ctx, payment.BookingID)
	if err != nil {
		return err
	}
	if !booking.IsActive() {
		return nil
	}
	if _, err := p.bookings.Cancel(ctx, booking.ID, domain.BookingCancelled); err != nil &&
		!errors.Is(err, domain.ErrBookingState) {
		return err
	}
	return nil
}

// settle moves a payment to a terminal status, tolerating a payment that
// another delivery already settled.
func (p *Payment) settle(ctx context.Context, payment domain.Payment, status, reason string) error {
	_, err := p.payments.Update(ctx, payment.ID, domain.PaymentUpdate{Status: status, FailureReason: reason})
	if errors.Is(err, domain.ErrPaymentState) {
		return nil
	}
	return err
}

// refundOrphanedCharge returns money taken for a booking that can no longer be
// honoured. A failure here is logged loudly rather than propagated: the webhook
// itself was processed, and retrying it would not fix the provider call.
func (p *Payment) refundOrphanedCharge(ctx context.Context, paymentID string, amount int64, providerRef string) {
	if _, err := p.provider.Refund(ctx, providerRef, amount); err != nil {
		p.log.ErrorContext(ctx, "automatic refund failed; manual intervention required",
			"payment_id", paymentID, "provider_ref", providerRef, "amount_cents", amount, "error", err)
		return
	}
	if _, err := p.payments.Update(ctx, paymentID, domain.PaymentUpdate{Status: domain.PaymentRefunded}); err != nil {
		p.log.ErrorContext(ctx, "refund succeeded but payment row not updated",
			"payment_id", paymentID, "error", err)
	}
}

// CancelBooking cancels a booking and refunds it when money changed hands.
// Held bookings are simply released; confirmed ones go through the provider.
func (p *Payment) CancelBooking(ctx context.Context, bookingID, userID string) (domain.Booking, error) {
	booking, err := p.bookings.GetByID(ctx, bookingID)
	if err != nil {
		return domain.Booking{}, err
	}
	if booking.UserID != userID {
		return domain.Booking{}, domain.ErrBookingNotFound
	}
	if !booking.IsActive() {
		return domain.Booking{}, domain.ErrBookingState
	}
	if booking.Status == domain.BookingConfirmed && booking.Showtime != nil {
		if time.Now().Add(p.policy.RefundCutoff).After(booking.Showtime.StartsAt) {
			return domain.Booking{}, fmt.Errorf("%w: too close to the screening to refund", domain.ErrBookingState)
		}
	}

	// Cancel any charge still in flight so the gateway does not capture money
	// for seats we are about to release.
	if open, err := p.payments.GetOpenByBooking(ctx, booking.ID); err == nil {
		if cancelErr := p.provider.Cancel(ctx, open.ProviderRef); cancelErr != nil {
			p.log.WarnContext(ctx, "could not cancel in-flight payment",
				"payment_id", open.ID, "error", cancelErr)
		}
		if _, err := p.payments.Update(ctx, open.ID, domain.PaymentUpdate{
			Status:        domain.PaymentCancelled,
			FailureReason: "booking cancelled by user",
		}); err != nil && !errors.Is(err, domain.ErrPaymentState) {
			return domain.Booking{}, err
		}
	} else if !errors.Is(err, domain.ErrPaymentNotFound) {
		return domain.Booking{}, err
	}

	// Refund a captured charge before releasing the seats, so we never free the
	// seats while still holding the customer's money.
	captured, err := p.payments.GetSucceededByBooking(ctx, booking.ID)
	switch {
	case err == nil:
		if _, refundErr := p.provider.Refund(ctx, captured.ProviderRef, captured.AmountCents); refundErr != nil {
			p.log.ErrorContext(ctx, "refund failed; booking left untouched",
				"booking_id", booking.ID, "payment_id", captured.ID, "error", refundErr)
			return domain.Booking{}, fmt.Errorf("%w: %s", domain.ErrProviderUnavailable, refundErr)
		}
		if _, err := p.payments.Update(ctx, captured.ID, domain.PaymentUpdate{Status: domain.PaymentRefunded}); err != nil &&
			!errors.Is(err, domain.ErrPaymentState) {
			return domain.Booking{}, err
		}
	case !errors.Is(err, domain.ErrPaymentNotFound):
		return domain.Booking{}, err
	}

	return p.bookings.Cancel(ctx, booking.ID, domain.BookingCancelled)
}

func stringOr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}
