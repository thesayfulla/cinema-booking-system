package domain

import (
	"context"
	"time"
)

// Movie is a film that can be screened.
type Movie struct {
	ID              string
	Slug            string
	Title           string
	Description     string
	DurationMinutes int
	PosterURL       string
}

// Hall is a physical auditorium containing seats.
type Hall struct {
	ID   string
	Name string
}

// Seat classes influence pricing on top of a showtime's base price.
const (
	SeatClassStandard   = "standard"
	SeatClassPremium    = "premium"
	SeatClassAccessible = "accessible"
)

// Seat is a physical seat in a hall. Seats belong to halls, not to showtimes:
// the same seat is sold once per showtime.
type Seat struct {
	ID         string
	HallID     string
	RowLabel   string
	SeatNumber int
	SeatClass  string
}

// PriceCents returns the price of this seat for a showtime's base price.
// Premium seats carry a surcharge; accessible seats are sold at the base price.
func (s Seat) PriceCents(basePriceCents int64) int64 {
	if s.SeatClass == SeatClassPremium {
		return basePriceCents + basePriceCents/4
	}
	return basePriceCents
}

// Showtime statuses.
const (
	ShowtimeScheduled = "scheduled"
	ShowtimeCancelled = "cancelled"
)

// Showtime is a screening of a movie in a hall at a point in time.
type Showtime struct {
	ID             string
	MovieID        string
	MovieTitle     string
	HallID         string
	HallName       string
	StartsAt       time.Time
	BasePriceCents int64
	Currency       string
	Status         string
}

// SeatAvailability describes a seat's state for one showtime.
type SeatAvailability struct {
	Seat       Seat
	Status     string // SeatStatusAvailable, SeatStatusHeld or SeatStatusSold
	PriceCents int64
	// HeldByUser is set when the seat is held or sold by the requesting user,
	// so the UI can distinguish "my seat" from "someone else's".
	HeldByUser bool
	BookingID  string
}

// Seat availability states, as seen by a client browsing a showtime.
const (
	SeatStatusAvailable = "available"
	SeatStatusHeld      = "held"
	SeatStatusSold      = "sold"
)

// CatalogRepository reads the screening catalog.
type CatalogRepository interface {
	ListMovies(ctx context.Context) ([]Movie, error)
	GetMovie(ctx context.Context, idOrSlug string) (Movie, error)
	// ListShowtimes returns scheduled showtimes starting at or after from.
	// A blank movieID lists showtimes across all movies.
	ListShowtimes(ctx context.Context, movieID string, from time.Time) ([]Showtime, error)
	GetShowtime(ctx context.Context, showtimeID string) (Showtime, error)
	// SeatMap returns every seat of the showtime's hall with its current status.
	// Seats held by bookings whose hold already lapsed are reported as available.
	SeatMap(ctx context.Context, showtimeID string, userID string) ([]SeatAvailability, error)
	// SeatsByIDs loads seats and verifies they all belong to hallID.
	// Returns ErrSeatNotFound if any id is unknown or in another hall.
	SeatsByIDs(ctx context.Context, hallID string, seatIDs []string) ([]Seat, error)
}
