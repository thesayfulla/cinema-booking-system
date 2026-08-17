package http

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
	"github.com/thesayfulla/cinema-booking-system/internal/metrics"
	"github.com/thesayfulla/cinema-booking-system/internal/usecase"
)

// IdempotencyHeader lets a client retry a create-booking request safely.
const IdempotencyHeader = "Idempotency-Key"

// BookingHandler serves the reservation endpoints.
type BookingHandler struct {
	bookings *usecase.Booking
	payments *usecase.Payment
	metrics  *metrics.Collector
	log      *slog.Logger
}

// NewBookingHandler builds the booking handler.
func NewBookingHandler(bookings *usecase.Booking, payments *usecase.Payment, m *metrics.Collector, log *slog.Logger) *BookingHandler {
	return &BookingHandler{bookings: bookings, payments: payments, metrics: m, log: log}
}

// Create handles POST /api/v1/bookings: it holds seats for the caller.
//
// A retried request that carries the same Idempotency-Key returns the original
// booking rather than reserving more seats.
func (h *BookingHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateBookingRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeRequestError(w, r, h.log, err)
		return
	}

	idempotencyKey := strings.TrimSpace(r.Header.Get(IdempotencyHeader))
	if len(idempotencyKey) > 200 {
		writeError(w, r, http.StatusBadRequest, codeValidation,
			"idempotency key is too long", IdempotencyHeader)
		return
	}

	booking, err := h.bookings.HoldSeats(r.Context(), usecase.HoldSeatsInput{
		ShowtimeID:     req.ShowtimeID,
		SeatIDs:        req.SeatIDs,
		UserID:         UserID(r.Context()),
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		if errors.Is(err, domain.ErrSeatUnavailable) {
			h.metrics.SeatConflict()
			h.metrics.BookingResult("seat_conflict")
		} else {
			h.metrics.BookingResult("rejected")
		}
		writeDomainError(w, r, h.log, err)
		return
	}

	h.metrics.BookingResult("held")
	h.log.InfoContext(r.Context(), "seats held",
		"booking_id", booking.ID, "reference", booking.Reference,
		"showtime_id", booking.ShowtimeID, "seats", len(booking.Seats))

	w.Header().Set("Location", "/api/v1/bookings/"+booking.ID)
	writeJSON(w, http.StatusCreated, toBookingResponse(booking))
}

// Get handles GET /api/v1/bookings/{bookingID}.
func (h *BookingHandler) Get(w http.ResponseWriter, r *http.Request) {
	booking, err := h.bookings.Get(r.Context(), r.PathValue("bookingID"), UserID(r.Context()))
	if err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toBookingResponse(booking))
}

// List handles GET /api/v1/bookings, returning the caller's own bookings.
func (h *BookingHandler) List(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, r, http.StatusBadRequest, codeValidation,
				"limit must be a positive integer", "limit")
			return
		}
		limit = parsed
	}

	bookings, err := h.bookings.ListForUser(r.Context(), UserID(r.Context()), limit)
	if err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}

	out := make([]BookingResponse, 0, len(bookings))
	for _, b := range bookings {
		out = append(out, toBookingResponse(b))
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookings": out})
}

// Cancel handles DELETE /api/v1/bookings/{bookingID}.
//
// It goes through the payment use case because cancelling a confirmed booking
// has to return the customer's money, not just the seats.
func (h *BookingHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	booking, err := h.payments.CancelBooking(r.Context(), r.PathValue("bookingID"), UserID(r.Context()))
	if err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}

	h.metrics.BookingResult("cancelled")
	h.log.InfoContext(r.Context(), "booking cancelled",
		"booking_id", booking.ID, "reference", booking.Reference)

	writeJSON(w, http.StatusOK, toBookingResponse(booking))
}
