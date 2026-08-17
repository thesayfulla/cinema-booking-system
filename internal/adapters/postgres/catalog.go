package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// CatalogRepository reads movies, showtimes and seat availability.
type CatalogRepository struct {
	db *DB
}

// NewCatalogRepository wires a catalog repository to the pool.
func NewCatalogRepository(db *DB) *CatalogRepository { return &CatalogRepository{db: db} }

func (r *CatalogRepository) ListMovies(ctx context.Context) ([]domain.Movie, error) {
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT id, slug, title, description, duration_minutes, poster_url
		FROM movies
		ORDER BY title`)
	if err != nil {
		return nil, fmt.Errorf("list movies: %w", err)
	}
	defer rows.Close()

	movies := make([]domain.Movie, 0, 8)
	for rows.Next() {
		var m domain.Movie
		if err := rows.Scan(&m.ID, &m.Slug, &m.Title, &m.Description, &m.DurationMinutes, &m.PosterURL); err != nil {
			return nil, fmt.Errorf("scan movie: %w", err)
		}
		movies = append(movies, m)
	}
	return movies, rows.Err()
}

// GetMovie accepts either a UUID or a slug so URLs can stay readable.
func (r *CatalogRepository) GetMovie(ctx context.Context, idOrSlug string) (domain.Movie, error) {
	var m domain.Movie
	err := r.db.q(ctx).QueryRow(ctx, `
		SELECT id, slug, title, description, duration_minutes, poster_url
		FROM movies
		WHERE slug = $1 OR id = $2::uuid`,
		idOrSlug, nullableUUID(idOrSlug),
	).Scan(&m.ID, &m.Slug, &m.Title, &m.Description, &m.DurationMinutes, &m.PosterURL)
	if isNoRows(err) {
		return domain.Movie{}, domain.ErrMovieNotFound
	}
	if err != nil {
		return domain.Movie{}, fmt.Errorf("get movie: %w", err)
	}
	return m, nil
}

func (r *CatalogRepository) ListShowtimes(ctx context.Context, movieID string, from time.Time) ([]domain.Showtime, error) {
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT s.id, s.movie_id, m.title, s.hall_id, h.name,
		       s.starts_at, s.base_price_cents, s.currency, s.status
		FROM showtimes s
		JOIN movies m ON m.id = s.movie_id
		JOIN halls  h ON h.id = s.hall_id
		WHERE s.status = 'scheduled'
		  AND s.starts_at >= $1
		  AND ($2::uuid IS NULL OR s.movie_id = $2::uuid)
		ORDER BY s.starts_at`,
		from, nullableUUID(movieID),
	)
	if err != nil {
		return nil, fmt.Errorf("list showtimes: %w", err)
	}
	defer rows.Close()

	showtimes := make([]domain.Showtime, 0, 16)
	for rows.Next() {
		s, err := scanShowtime(rows)
		if err != nil {
			return nil, err
		}
		showtimes = append(showtimes, s)
	}
	return showtimes, rows.Err()
}

func (r *CatalogRepository) GetShowtime(ctx context.Context, showtimeID string) (domain.Showtime, error) {
	if !isUUID(showtimeID) {
		return domain.Showtime{}, domain.ErrShowtimeNotFound
	}

	s, err := scanShowtime(r.db.q(ctx).QueryRow(ctx, `
		SELECT s.id, s.movie_id, m.title, s.hall_id, h.name,
		       s.starts_at, s.base_price_cents, s.currency, s.status
		FROM showtimes s
		JOIN movies m ON m.id = s.movie_id
		JOIN halls  h ON h.id = s.hall_id
		WHERE s.id = $1::uuid`, showtimeID))
	if isNoRows(err) {
		return domain.Showtime{}, domain.ErrShowtimeNotFound
	}
	if err != nil {
		return domain.Showtime{}, err
	}
	return s, nil
}

// SeatMap returns every seat in the showtime's hall alongside its status.
//
// A seat counts as taken only while an active claim backs it, and a held claim
// whose booking already lapsed is reported as available even if the expiry
// sweeper has not run yet — so the seat map never shows a stale hold.
func (r *CatalogRepository) SeatMap(ctx context.Context, showtimeID string, userID string) ([]domain.SeatAvailability, error) {
	if !isUUID(showtimeID) {
		return nil, domain.ErrShowtimeNotFound
	}

	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT seat.id, seat.hall_id, seat.row_label, seat.seat_number, seat.seat_class,
		       st.base_price_cents,
		       b.id, b.status, b.user_id, b.hold_expires_at
		FROM showtimes st
		JOIN seats seat ON seat.hall_id = st.hall_id
		LEFT JOIN booking_seats bs
		       ON bs.seat_id = seat.id AND bs.showtime_id = st.id AND bs.active
		LEFT JOIN bookings b
		       ON b.id = bs.booking_id
		      AND (b.status = 'confirmed'
		           OR (b.status = 'held' AND b.hold_expires_at > now()))
		WHERE st.id = $1::uuid
		ORDER BY seat.row_label, seat.seat_number`,
		showtimeID,
	)
	if err != nil {
		return nil, fmt.Errorf("seat map: %w", err)
	}
	defer rows.Close()

	seats := make([]domain.SeatAvailability, 0, 64)
	for rows.Next() {
		var (
			sa            domain.SeatAvailability
			basePrice     int64
			bookingID     *string
			bookingStatus *string
			bookingUser   *string
			holdExpires   *time.Time
		)
		if err := rows.Scan(
			&sa.Seat.ID, &sa.Seat.HallID, &sa.Seat.RowLabel, &sa.Seat.SeatNumber, &sa.Seat.SeatClass,
			&basePrice, &bookingID, &bookingStatus, &bookingUser, &holdExpires,
		); err != nil {
			return nil, fmt.Errorf("scan seat: %w", err)
		}

		sa.PriceCents = sa.Seat.PriceCents(basePrice)
		sa.Status = domain.SeatStatusAvailable
		if bookingStatus != nil {
			if *bookingStatus == domain.BookingConfirmed {
				sa.Status = domain.SeatStatusSold
			} else {
				sa.Status = domain.SeatStatusHeld
			}
			sa.BookingID = derefString(bookingID)
			sa.HeldByUser = userID != "" && derefString(bookingUser) == userID
		}
		seats = append(seats, sa)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(seats) == 0 {
		// No rows means the showtime does not exist: a hall always has seats.
		return nil, domain.ErrShowtimeNotFound
	}
	return seats, nil
}

// SeatsByIDs loads the requested seats and fails unless all of them exist in
// the given hall, which prevents booking a seat from another auditorium.
func (r *CatalogRepository) SeatsByIDs(ctx context.Context, hallID string, seatIDs []string) ([]domain.Seat, error) {
	if len(seatIDs) == 0 {
		return nil, domain.Invalid("seat_ids", "at least one seat is required")
	}
	for _, id := range seatIDs {
		if !isUUID(id) {
			return nil, domain.ErrSeatNotFound
		}
	}

	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT id, hall_id, row_label, seat_number, seat_class
		FROM seats
		WHERE hall_id = $1::uuid AND id = ANY($2::uuid[])
		ORDER BY row_label, seat_number`,
		hallID, seatIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("load seats: %w", err)
	}
	defer rows.Close()

	seats := make([]domain.Seat, 0, len(seatIDs))
	for rows.Next() {
		var s domain.Seat
		if err := rows.Scan(&s.ID, &s.HallID, &s.RowLabel, &s.SeatNumber, &s.SeatClass); err != nil {
			return nil, fmt.Errorf("scan seat: %w", err)
		}
		seats = append(seats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(seats) != len(seatIDs) {
		return nil, domain.ErrSeatNotFound
	}
	return seats, nil
}

// scanner is satisfied by both pgx.Row and pgx.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanShowtime(row scanner) (domain.Showtime, error) {
	var s domain.Showtime
	if err := row.Scan(&s.ID, &s.MovieID, &s.MovieTitle, &s.HallID, &s.HallName,
		&s.StartsAt, &s.BasePriceCents, &s.Currency, &s.Status); err != nil {
		return domain.Showtime{}, err
	}
	return s, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// nullableUUID turns an empty filter into SQL NULL so one query can serve both
// the filtered and unfiltered cases.
func nullableUUID(id string) *string {
	if id == "" || !isUUID(id) {
		return nil
	}
	return &id
}

// isUUID reports whether s looks like a UUID. Checking in Go keeps a malformed
// path parameter from reaching Postgres as a cast error.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
