package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
	"github.com/thesayfulla/cinema-booking-system/internal/usecase"
)

// BookingHandler handles HTTP requests related to booking.
// It depends only on the usecase layer (no direct storage access).
type BookingHandler struct {
	bookingUC *usecase.BookingUsecase
}

// NewBookingHandler creates a new booking HTTP handler.
func NewBookingHandler(bookingUC *usecase.BookingUsecase) *BookingHandler {
	return &BookingHandler{bookingUC: bookingUC}
}

// HoldSeat handles POST /movies/{movieID}/seats/{seatID}/hold
// Request body: {"user_id": "user123"}
// Response: 201 Created with session details
func (h *BookingHandler) HoldSeat(w http.ResponseWriter, r *http.Request) {
	movieID := r.PathValue("movieID")
	seatID := r.PathValue("seatID")

	var req HoldSeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	booking, err := h.bookingUC.HoldSeat(r.Context(), movieID, seatID, req.UserID)
	if err != nil {
		if errors.Is(err, domain.ErrSeatAlreadyBooked) {
			writeError(w, http.StatusConflict, "seat is already booked")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to hold seat")
		return
	}

	response := HoldSeatResponse{
		SessionID: booking.ID,
		MovieID:   booking.MovieID,
		SeatID:    booking.SeatID,
		ExpiresAt: booking.ExpiresAt.Format(time.RFC3339),
	}

	writeJSON(w, http.StatusCreated, response)
}

// ListSeats handles GET /movies/{movieID}/seats
// Response: 200 OK with array of seat statuses
func (h *BookingHandler) ListSeats(w http.ResponseWriter, r *http.Request) {
	movieID := r.PathValue("movieID")

	bookings, err := h.bookingUC.ListSeats(r.Context(), movieID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list seats")
		return
	}

	seats := make(ListSeatsResponse, 0, len(bookings))
	for _, booking := range bookings {
		seats = append(seats, SeatInfo{
			SeatID:    booking.SeatID,
			UserID:    booking.UserID,
			Booked:    true,
			Confirmed: booking.Status == "confirmed",
		})
	}

	writeJSON(w, http.StatusOK, seats)
}

// ConfirmSession handles PUT /sessions/{sessionID}/confirm
// Request body: {"user_id": "user123"}
// Response: 200 OK with confirmed session details
func (h *BookingHandler) ConfirmSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")

	var req ConfirmSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	booking, err := h.bookingUC.ConfirmSession(r.Context(), sessionID, req.UserID)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		if errors.Is(err, domain.ErrUnauthorized) {
			writeError(w, http.StatusForbidden, "unauthorized")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to confirm session")
		return
	}

	response := ConfirmSessionResponse{
		SessionID: booking.ID,
		MovieID:   booking.MovieID,
		SeatID:    booking.SeatID,
		UserID:    booking.UserID,
		Status:    booking.Status,
	}

	writeJSON(w, http.StatusOK, response)
}

// ReleaseSession handles DELETE /sessions/{sessionID}
// Request body: {"user_id": "user123"}
// Response: 204 No Content
func (h *BookingHandler) ReleaseSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")

	var req ReleaseSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	err := h.bookingUC.ReleaseSession(r.Context(), sessionID, req.UserID)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		if errors.Is(err, domain.ErrUnauthorized) {
			writeError(w, http.StatusForbidden, "unauthorized")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to release session")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Helper functions

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, ErrorResponse{Error: message})
}
