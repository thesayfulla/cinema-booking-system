package http

import (
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// Requests

// CreateBookingRequest holds seats for the caller.
type CreateBookingRequest struct {
	ShowtimeID string   `json:"showtime_id"`
	SeatIDs    []string `json:"seat_ids"`
}

// Responses

// MovieResponse describes a film.
type MovieResponse struct {
	ID              string `json:"id"`
	Slug            string `json:"slug"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	DurationMinutes int    `json:"duration_minutes"`
	PosterURL       string `json:"poster_url,omitempty"`
}

// ShowtimeResponse describes a screening.
type ShowtimeResponse struct {
	ID             string    `json:"id"`
	MovieID        string    `json:"movie_id"`
	MovieTitle     string    `json:"movie_title"`
	HallID         string    `json:"hall_id"`
	HallName       string    `json:"hall_name"`
	StartsAt       time.Time `json:"starts_at"`
	BasePriceCents int64     `json:"base_price_cents"`
	Currency       string    `json:"currency"`
	Status         string    `json:"status"`
}

// SeatResponse is one seat on a showtime's seat map.
type SeatResponse struct {
	ID         string `json:"id"`
	RowLabel   string `json:"row_label"`
	SeatNumber int    `json:"seat_number"`
	SeatClass  string `json:"seat_class"`
	Status     string `json:"status"`
	PriceCents int64  `json:"price_cents"`
	Mine       bool   `json:"mine"`
}

// SeatMapResponse is a showtime together with its seats.
type SeatMapResponse struct {
	Showtime ShowtimeResponse `json:"showtime"`
	Seats    []SeatResponse   `json:"seats"`
}

// BookedSeatResponse is a seat inside a booking.
type BookedSeatResponse struct {
	SeatID     string `json:"seat_id"`
	RowLabel   string `json:"row_label"`
	SeatNumber int    `json:"seat_number"`
	SeatClass  string `json:"seat_class"`
	PriceCents int64  `json:"price_cents"`
}

// BookingResponse is a booking as returned by the API.
type BookingResponse struct {
	ID               string               `json:"id"`
	Reference        string               `json:"reference"`
	ShowtimeID       string               `json:"showtime_id"`
	UserID           string               `json:"user_id"`
	Status           string               `json:"status"`
	TotalAmountCents int64                `json:"total_amount_cents"`
	Currency         string               `json:"currency"`
	ExpiresAt        *time.Time           `json:"expires_at,omitempty"`
	Seats            []BookedSeatResponse `json:"seats"`
	Showtime         *ShowtimeResponse    `json:"showtime,omitempty"`
	CreatedAt        time.Time            `json:"created_at"`
}

// PaymentResponse is a payment as returned by the API.
type PaymentResponse struct {
	ID          string `json:"id"`
	BookingID   string `json:"booking_id"`
	Provider    string `json:"provider"`
	ProviderRef string `json:"provider_ref,omitempty"`
	Status      string `json:"status"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
	// ClientSecret and NextActionURL are what the client needs to complete the
	// charge at the provider. They are only present when checkout starts.
	ClientSecret  string `json:"client_secret,omitempty"`
	NextActionURL string `json:"next_action_url,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
}

// Mappers

func toMovieResponse(m domain.Movie) MovieResponse {
	return MovieResponse{
		ID:              m.ID,
		Slug:            m.Slug,
		Title:           m.Title,
		Description:     m.Description,
		DurationMinutes: m.DurationMinutes,
		PosterURL:       m.PosterURL,
	}
}

func toShowtimeResponse(s domain.Showtime) ShowtimeResponse {
	return ShowtimeResponse{
		ID:             s.ID,
		MovieID:        s.MovieID,
		MovieTitle:     s.MovieTitle,
		HallID:         s.HallID,
		HallName:       s.HallName,
		StartsAt:       s.StartsAt,
		BasePriceCents: s.BasePriceCents,
		Currency:       s.Currency,
		Status:         s.Status,
	}
}

func toSeatMapResponse(showtime domain.Showtime, seats []domain.SeatAvailability) SeatMapResponse {
	out := SeatMapResponse{
		Showtime: toShowtimeResponse(showtime),
		Seats:    make([]SeatResponse, 0, len(seats)),
	}
	for _, s := range seats {
		out.Seats = append(out.Seats, SeatResponse{
			ID:         s.Seat.ID,
			RowLabel:   s.Seat.RowLabel,
			SeatNumber: s.Seat.SeatNumber,
			SeatClass:  s.Seat.SeatClass,
			Status:     s.Status,
			PriceCents: s.PriceCents,
			Mine:       s.HeldByUser,
		})
	}
	return out
}

func toBookingResponse(b domain.Booking) BookingResponse {
	resp := BookingResponse{
		ID:               b.ID,
		Reference:        b.Reference,
		ShowtimeID:       b.ShowtimeID,
		UserID:           b.UserID,
		Status:           b.Status,
		TotalAmountCents: b.TotalAmountCents,
		Currency:         b.Currency,
		ExpiresAt:        b.HoldExpiresAt,
		Seats:            make([]BookedSeatResponse, 0, len(b.Seats)),
		CreatedAt:        b.CreatedAt,
	}
	for _, s := range b.Seats {
		// Released seats stay in the database for auditing but are not part of
		// what the customer holds.
		if !s.Active {
			continue
		}
		resp.Seats = append(resp.Seats, BookedSeatResponse{
			SeatID:     s.SeatID,
			RowLabel:   s.RowLabel,
			SeatNumber: s.SeatNumber,
			SeatClass:  s.SeatClass,
			PriceCents: s.PriceCents,
		})
	}
	if b.Showtime != nil {
		st := toShowtimeResponse(*b.Showtime)
		resp.Showtime = &st
	}
	return resp
}

func toPaymentResponse(p domain.Payment) PaymentResponse {
	return PaymentResponse{
		ID:            p.ID,
		BookingID:     p.BookingID,
		Provider:      p.Provider,
		ProviderRef:   p.ProviderRef,
		Status:        p.Status,
		AmountCents:   p.AmountCents,
		Currency:      p.Currency,
		FailureReason: p.FailureReason,
	}
}
