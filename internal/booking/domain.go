package booking

import "errors"

var (
	ErrSeatAlreadyBooked = errors.New("seat already booked")
)

type Booking struct {
	ID      string
	MovieID string
	SeatID  string
	UserID  string
	Status  string
}

type BookingStore interface {
	Book(b Booking) error
	ListBookings(movieID string) []Booking
}
