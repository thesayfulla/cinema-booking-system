package domain

import (
	"context"
	"time"
)

// Payment lifecycle. pending/processing are open states (the booking's seats
// stay held); the rest are terminal.
const (
	PaymentPending    = "pending"
	PaymentProcessing = "processing"
	PaymentSucceeded  = "succeeded"
	PaymentFailed     = "failed"
	PaymentCancelled  = "cancelled"
	PaymentRefunded   = "refunded"
)

// Payment records a charge attempt against a booking.
type Payment struct {
	ID             string
	BookingID      string
	Provider       string
	ProviderRef    string
	Status         string
	AmountCents    int64
	Currency       string
	IdempotencyKey string
	FailureReason  string
	Metadata       map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// IsOpen reports whether the payment is still in flight.
func (p Payment) IsOpen() bool {
	return p.Status == PaymentPending || p.Status == PaymentProcessing
}

// NewPayment describes a payment row to create.
type NewPayment struct {
	BookingID      string
	Provider       string
	ProviderRef    string
	Status         string
	AmountCents    int64
	Currency       string
	IdempotencyKey string
	Metadata       map[string]any
}

// PaymentUpdate carries a status transition for a payment.
type PaymentUpdate struct {
	Status        string
	ProviderRef   string
	FailureReason string
}

// PaymentRepository persists payments and the provider events that drive them.
type PaymentRepository interface {
	Create(ctx context.Context, p NewPayment) (Payment, error)
	GetByID(ctx context.Context, paymentID string) (Payment, error)
	GetByProviderRef(ctx context.Context, provider, ref string) (Payment, error)
	// GetOpenByBooking returns the booking's in-flight payment, if any.
	GetOpenByBooking(ctx context.Context, bookingID string) (Payment, error)
	// GetSucceededByBooking returns the booking's captured payment, if any.
	GetSucceededByBooking(ctx context.Context, bookingID string) (Payment, error)
	ListByBooking(ctx context.Context, bookingID string) ([]Payment, error)
	Update(ctx context.Context, paymentID string, u PaymentUpdate) (Payment, error)
	// RecordEvent stores a provider callback. It returns false when the event
	// was already recorded, which makes webhook redelivery a no-op.
	RecordEvent(ctx context.Context, e PaymentEvent) (bool, error)
}

// PaymentEvent is a callback received from a payment provider.
type PaymentEvent struct {
	Provider    string
	EventID     string
	Type        string
	ProviderRef string
	Payload     map[string]any
}

// Payment event types a provider may report. Providers translate their own
// vocabulary into these, so the use case layer stays provider-agnostic.
const (
	EventPaymentSucceeded = "payment.succeeded"
	EventPaymentFailed    = "payment.failed"
	EventPaymentCancelled = "payment.cancelled"
	EventPaymentRefunded  = "payment.refunded"
)

// PaymentIntentRequest asks a provider to start a charge.
type PaymentIntentRequest struct {
	BookingID   string
	Reference   string
	UserID      string
	AmountCents int64
	Currency    string
	Description string
	// IdempotencyKey must make retries of the same request safe at the provider.
	IdempotencyKey string
	Metadata       map[string]string
}

// PaymentIntent is a provider's handle on a charge in progress.
type PaymentIntent struct {
	ProviderRef string
	Status      string // one of the Payment* statuses
	// ClientSecret is whatever the client needs to complete the charge
	// (a Stripe client secret, a redirect URL, ...). Empty when not applicable.
	ClientSecret string
	// NextActionURL is where the client should send the user to pay, if the
	// provider requires a redirect.
	NextActionURL string
}

// RefundResult is the outcome of a refund request.
type RefundResult struct {
	ProviderRef string
	Status      string
}

// PaymentProvider abstracts a payment gateway. The mock provider implements it
// today; a real gateway (Stripe, Adyen, Click, Payme, ...) implements the same
// interface without any change above this layer.
type PaymentProvider interface {
	// Name identifies the provider in persisted rows, e.g. "mock" or "stripe".
	Name() string
	// CreateIntent starts a charge. Calling it twice with the same
	// IdempotencyKey must return the same intent.
	CreateIntent(ctx context.Context, req PaymentIntentRequest) (PaymentIntent, error)
	// Refund returns money for a previously succeeded charge.
	Refund(ctx context.Context, providerRef string, amountCents int64) (RefundResult, error)
	// Cancel aborts a charge that has not completed.
	Cancel(ctx context.Context, providerRef string) error
	// ParseWebhook authenticates and decodes a provider callback. It must
	// reject payloads whose signature does not verify.
	ParseWebhook(ctx context.Context, headers map[string]string, body []byte) (PaymentEvent, error)
}
