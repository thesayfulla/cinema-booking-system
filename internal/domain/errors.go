package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by the domain and use case layers. Adapters map
// these onto transport-specific responses (HTTP status codes, for instance);
// nothing below this layer knows about HTTP.
var (
	ErrMovieNotFound    = errors.New("movie not found")
	ErrShowtimeNotFound = errors.New("showtime not found")
	ErrShowtimeStarted  = errors.New("showtime has already started")
	ErrShowtimeCanceled = errors.New("showtime is cancelled")
	ErrSeatNotFound     = errors.New("seat not found in this hall")
	ErrSeatUnavailable  = errors.New("one or more seats are no longer available")

	ErrBookingNotFound = errors.New("booking not found")
	ErrBookingExpired  = errors.New("booking hold has expired")
	ErrBookingState    = errors.New("booking is not in a state that allows this operation")
	ErrUnauthorized    = errors.New("not authorized for this resource")

	ErrPaymentNotFound     = errors.New("payment not found")
	ErrPaymentState        = errors.New("payment is not in a state that allows this operation")
	ErrPaymentInProgress   = errors.New("a payment is already in progress for this booking")
	ErrWebhookUnverified   = errors.New("webhook signature verification failed")
	ErrProviderUnavailable = errors.New("payment provider is unavailable")

	ErrValidation = errors.New("validation failed")
)

// ValidationError describes a rejected input field. It wraps ErrValidation so
// callers can match with errors.Is while keeping the field detail.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func (e *ValidationError) Unwrap() error { return ErrValidation }

// Invalid builds a ValidationError for a field.
func Invalid(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}
