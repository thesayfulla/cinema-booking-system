package http

// HoldSeatRequest is the payload for POST /movies/{movieID}/seats/{seatID}/hold
type HoldSeatRequest struct {
	UserID string `json:"user_id"`
}

// HoldSeatResponse is returned after successfully holding a seat
type HoldSeatResponse struct {
	SessionID string `json:"session_id"`
	MovieID   string `json:"movie_id"`
	SeatID    string `json:"seat_id"`
	ExpiresAt string `json:"expires_at"`
}

// ConfirmSessionRequest is the payload for PUT /sessions/{sessionID}/confirm
type ConfirmSessionRequest struct {
	UserID string `json:"user_id"`
}

// ConfirmSessionResponse is returned after confirming a booking
type ConfirmSessionResponse struct {
	SessionID string `json:"session_id"`
	MovieID   string `json:"movie_id"`
	SeatID    string `json:"seat_id"`
	UserID    string `json:"user_id"`
	Status    string `json:"status"`
}

// ReleaseSessionRequest is the payload for DELETE /sessions/{sessionID}
type ReleaseSessionRequest struct {
	UserID string `json:"user_id"`
}

// SeatInfo represents a seat's current status
type SeatInfo struct {
	SeatID    string `json:"seat_id"`
	UserID    string `json:"user_id"`
	Booked    bool   `json:"booked"`
	Confirmed bool   `json:"confirmed"`
}

// ListSeatsResponse is the collection of seat statuses for a movie
type ListSeatsResponse []SeatInfo

// MovieResponse represents a movie for the API
type MovieResponse struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Rows        int    `json:"rows"`
	SeatsPerRow int    `json:"seats_per_row"`
}

// ErrorResponse is returned for any error
type ErrorResponse struct {
	Error string `json:"error"`
}
