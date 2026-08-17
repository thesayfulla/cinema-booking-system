package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// BookingRepository persists bookings and their seat claims.
type BookingRepository struct {
	db *DB
}

// NewBookingRepository wires a booking repository to the pool.
func NewBookingRepository(db *DB) *BookingRepository { return &BookingRepository{db: db} }

// holdMaxAttempts bounds the deadlock retry loop in Hold.
const holdMaxAttempts = 3

// Hold claims seats for a user, atomically.
//
// The insert into booking_seats is the whole concurrency story: the partial
// unique index booking_seats_one_active_claim_idx admits exactly one active
// claim per (showtime, seat), so two simultaneous requests for the same seat
// resolve to one winner and one unique violation — no application lock, no
// read-then-write race. Because the booking row and its seats are written in
// one transaction, a partially claimed booking cannot exist.
func (r *BookingRepository) Hold(ctx context.Context, req domain.NewBooking) (domain.Booking, error) {
	// Claim seats in a stable order. Two requests overlapping on several seats
	// would otherwise be able to take the index locks in opposite orders and
	// deadlock instead of one of them cleanly losing.
	seats := append([]domain.Seat(nil), req.Seats...)
	sort.Slice(seats, func(i, j int) bool { return seats[i].ID < seats[j].ID })

	var booking domain.Booking
	var err error
	for attempt := 1; ; attempt++ {
		err = r.db.WithTx(ctx, func(ctx context.Context) error {
			var holdErr error
			booking, holdErr = r.hold(ctx, req, seats)
			return holdErr
		})
		retryable := isRetryable(err) || uniqueViolation(err, "bookings_reference_key")
		if err == nil || !retryable || attempt == holdMaxAttempts {
			break
		}
	}

	// A concurrent replay of the same request created the booking first. The
	// transaction that lost is already rolled back, so the lookup runs here,
	// outside it: any query on an aborted transaction would fail with 25P02.
	if errors.Is(err, errIdempotentReplay) {
		return r.GetByIdempotencyKey(ctx, req.UserID, req.IdempotencyKey)
	}
	return booking, err
}

// errIdempotentReplay signals that the insert lost to a concurrent request
// carrying the same idempotency key. It never leaves this package.
var errIdempotentReplay = errors.New("idempotent replay")

func (r *BookingRepository) hold(ctx context.Context, req domain.NewBooking, seats []domain.Seat) (domain.Booking, error) {
	q := r.db.q(ctx)

	// Release seats whose holds lapsed for this showtime before claiming, so a
	// booking never fails against an expired hold just because the background
	// sweeper has not caught up yet.
	if err := r.releaseLapsedForShowtime(ctx, req.ShowtimeID); err != nil {
		return domain.Booking{}, err
	}

	var total int64
	for _, s := range seats {
		total += s.PriceCents(req.BasePriceCents)
	}

	reference, err := newBookingReference()
	if err != nil {
		return domain.Booking{}, err
	}

	expiresAt := time.Now().Add(req.HoldTTL)
	var booking domain.Booking
	err = q.QueryRow(ctx, `
		INSERT INTO bookings (reference, showtime_id, user_id, status,
		                      total_amount_cents, currency, hold_expires_at, idempotency_key)
		VALUES ($1, $2::uuid, $3, 'held', $4, $5, $6, $7)
		RETURNING id, reference, showtime_id, user_id, status, total_amount_cents,
		          currency, hold_expires_at, coalesce(idempotency_key, ''),
		          confirmed_at, cancelled_at, created_at, updated_at`,
		reference, req.ShowtimeID, req.UserID, total, req.Currency, expiresAt,
		nullableString(req.IdempotencyKey),
	).Scan(
		&booking.ID, &booking.Reference, &booking.ShowtimeID, &booking.UserID, &booking.Status,
		&booking.TotalAmountCents, &booking.Currency, &booking.HoldExpiresAt, &booking.IdempotencyKey,
		&booking.ConfirmedAt, &booking.CancelledAt, &booking.CreatedAt, &booking.UpdatedAt,
	)
	switch {
	case uniqueViolation(err, "bookings_idempotency_idx"):
		// A concurrent replay of the same request won the race. Unwind this
		// transaction and let Hold hand back the booking the winner created.
		return domain.Booking{}, errIdempotentReplay
	case err != nil:
		return domain.Booking{}, fmt.Errorf("insert booking: %w", err)
	}

	for _, seat := range seats {
		price := seat.PriceCents(req.BasePriceCents)
		_, err := q.Exec(ctx, `
			INSERT INTO booking_seats (booking_id, showtime_id, seat_id, price_cents, active)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, true)`,
			booking.ID, req.ShowtimeID, seat.ID, price,
		)
		if uniqueViolation(err, "booking_seats_one_active_claim_idx") {
			// Someone else holds this seat. Returning an error rolls the whole
			// transaction back, including the booking row and any seat claimed
			// a moment ago.
			return domain.Booking{}, domain.ErrSeatUnavailable
		}
		if err != nil {
			return domain.Booking{}, fmt.Errorf("claim seat %s: %w", seat.ID, err)
		}

		booking.Seats = append(booking.Seats, domain.BookedSeat{
			SeatID:     seat.ID,
			RowLabel:   seat.RowLabel,
			SeatNumber: seat.SeatNumber,
			SeatClass:  seat.SeatClass,
			PriceCents: price,
			Active:     true,
		})
	}

	return booking, nil
}

// releaseLapsedForShowtime expires held bookings of one showtime whose hold
// window passed, freeing their seats.
func (r *BookingRepository) releaseLapsedForShowtime(ctx context.Context, showtimeID string) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		WITH lapsed AS (
			UPDATE bookings
			SET status = 'expired', hold_expires_at = NULL, updated_at = now()
			WHERE showtime_id = $1::uuid
			  AND status = 'held'
			  AND hold_expires_at <= now()
			RETURNING id
		)
		UPDATE booking_seats bs
		SET active = false
		FROM lapsed
		WHERE bs.booking_id = lapsed.id AND bs.active`,
		showtimeID,
	)
	if err != nil {
		return fmt.Errorf("release lapsed holds: %w", err)
	}
	return nil
}

// ExpireDueHolds releases lapsed holds across all showtimes. The background
// sweeper calls it; the limit keeps each pass bounded on a busy system.
func (r *BookingRepository) ExpireDueHolds(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 500
	}

	tag, err := r.db.q(ctx).Exec(ctx, `
		WITH due AS (
			SELECT id FROM bookings
			WHERE status = 'held' AND hold_expires_at <= now()
			ORDER BY hold_expires_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		), expired AS (
			UPDATE bookings b
			SET status = 'expired', hold_expires_at = NULL, updated_at = now()
			FROM due
			WHERE b.id = due.id
			RETURNING b.id
		)
		UPDATE booking_seats bs
		SET active = false
		FROM expired
		WHERE bs.booking_id = expired.id AND bs.active`,
		limit,
	)
	if err != nil {
		return 0, fmt.Errorf("expire due holds: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

const bookingColumns = `
	b.id, b.reference, b.showtime_id, b.user_id, b.status, b.total_amount_cents,
	b.currency, b.hold_expires_at, coalesce(b.idempotency_key, ''),
	b.confirmed_at, b.cancelled_at, b.created_at, b.updated_at`

func (r *BookingRepository) GetByID(ctx context.Context, bookingID string) (domain.Booking, error) {
	if !isUUID(bookingID) {
		return domain.Booking{}, domain.ErrBookingNotFound
	}

	booking, err := scanBooking(r.db.q(ctx).QueryRow(ctx,
		`SELECT `+bookingColumns+` FROM bookings b WHERE b.id = $1::uuid`, bookingID))
	if isNoRows(err) {
		return domain.Booking{}, domain.ErrBookingNotFound
	}
	if err != nil {
		return domain.Booking{}, fmt.Errorf("get booking: %w", err)
	}

	if err := r.loadSeats(ctx, &booking); err != nil {
		return domain.Booking{}, err
	}
	if err := r.loadShowtime(ctx, &booking); err != nil {
		return domain.Booking{}, err
	}
	return booking, nil
}

func (r *BookingRepository) GetByIdempotencyKey(ctx context.Context, userID, key string) (domain.Booking, error) {
	if key == "" {
		return domain.Booking{}, domain.ErrBookingNotFound
	}

	booking, err := scanBooking(r.db.q(ctx).QueryRow(ctx,
		`SELECT `+bookingColumns+` FROM bookings b
		 WHERE b.user_id = $1 AND b.idempotency_key = $2`, userID, key))
	if isNoRows(err) {
		return domain.Booking{}, domain.ErrBookingNotFound
	}
	if err != nil {
		return domain.Booking{}, fmt.Errorf("get booking by idempotency key: %w", err)
	}

	if err := r.loadSeats(ctx, &booking); err != nil {
		return domain.Booking{}, err
	}
	if err := r.loadShowtime(ctx, &booking); err != nil {
		return domain.Booking{}, err
	}
	return booking, nil
}

// LockForUpdate reads a booking with a row lock so that confirm, cancel and
// payment callbacks cannot interleave on the same booking.
func (r *BookingRepository) LockForUpdate(ctx context.Context, bookingID string) (domain.Booking, error) {
	if !isUUID(bookingID) {
		return domain.Booking{}, domain.ErrBookingNotFound
	}

	booking, err := scanBooking(r.db.q(ctx).QueryRow(ctx,
		`SELECT `+bookingColumns+` FROM bookings b WHERE b.id = $1::uuid FOR UPDATE`, bookingID))
	if isNoRows(err) {
		return domain.Booking{}, domain.ErrBookingNotFound
	}
	if err != nil {
		return domain.Booking{}, fmt.Errorf("lock booking: %w", err)
	}
	if err := r.loadSeats(ctx, &booking); err != nil {
		return domain.Booking{}, err
	}
	return booking, nil
}

func (r *BookingRepository) ListByUser(ctx context.Context, userID string, limit int) ([]domain.Booking, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := r.db.q(ctx).Query(ctx,
		`SELECT `+bookingColumns+` FROM bookings b
		 WHERE b.user_id = $1
		 ORDER BY b.created_at DESC
		 LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list bookings: %w", err)
	}
	defer rows.Close()

	bookings := make([]domain.Booking, 0, limit)
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			return nil, fmt.Errorf("scan booking: %w", err)
		}
		bookings = append(bookings, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range bookings {
		if err := r.loadSeats(ctx, &bookings[i]); err != nil {
			return nil, err
		}
		if err := r.loadShowtime(ctx, &bookings[i]); err != nil {
			return nil, err
		}
	}
	return bookings, nil
}

// Confirm makes a held booking permanent. It only matches held rows, so a
// double confirm (two webhook deliveries, say) leaves the first result intact.
func (r *BookingRepository) Confirm(ctx context.Context, bookingID string) (domain.Booking, error) {
	booking, err := scanBooking(r.db.q(ctx).QueryRow(ctx, `
		UPDATE bookings b
		SET status = 'confirmed', confirmed_at = now(), hold_expires_at = NULL, updated_at = now()
		WHERE b.id = $1::uuid AND b.status = 'held'
		RETURNING `+bookingColumns, bookingID))
	if isNoRows(err) {
		return domain.Booking{}, domain.ErrBookingState
	}
	if err != nil {
		return domain.Booking{}, fmt.Errorf("confirm booking: %w", err)
	}
	if err := r.loadSeats(ctx, &booking); err != nil {
		return domain.Booking{}, err
	}
	return booking, nil
}

// Cancel moves an active booking to a terminal state and frees its seats.
// status is domain.BookingCancelled or domain.BookingExpired.
func (r *BookingRepository) Cancel(ctx context.Context, bookingID string, status string) (domain.Booking, error) {
	if status != domain.BookingCancelled && status != domain.BookingExpired {
		return domain.Booking{}, fmt.Errorf("cancel booking: invalid target status %q", status)
	}

	if !isUUID(bookingID) {
		return domain.Booking{}, domain.ErrBookingNotFound
	}

	// Freeing the seats and moving the booking to its terminal state must land
	// together, or a crash between them would leave seats claimed forever.
	var booking domain.Booking
	err := r.db.WithTx(ctx, func(ctx context.Context) error {
		b, err := scanBooking(r.db.q(ctx).QueryRow(ctx, `
			UPDATE bookings b
			SET status = $2, cancelled_at = now(), hold_expires_at = NULL, updated_at = now()
			WHERE b.id = $1::uuid AND b.status IN ('held', 'confirmed')
			RETURNING `+bookingColumns, bookingID, status))
		if isNoRows(err) {
			return domain.ErrBookingState
		}
		if err != nil {
			return fmt.Errorf("cancel booking: %w", err)
		}

		if _, err := r.db.q(ctx).Exec(ctx, `
			UPDATE booking_seats SET active = false
			WHERE booking_id = $1::uuid AND active`, bookingID); err != nil {
			return fmt.Errorf("release seats: %w", err)
		}

		if err := r.loadSeats(ctx, &b); err != nil {
			return err
		}
		booking = b
		return nil
	})
	return booking, err
}

// ExtendHold pushes out a held booking's expiry, giving a checkout in progress
// room to finish without the seats being swept away.
func (r *BookingRepository) ExtendHold(ctx context.Context, bookingID string, until time.Time) error {
	tag, err := r.db.q(ctx).Exec(ctx, `
		UPDATE bookings
		SET hold_expires_at = GREATEST(hold_expires_at, $2), updated_at = now()
		WHERE id = $1::uuid AND status = 'held'`,
		bookingID, until,
	)
	if err != nil {
		return fmt.Errorf("extend hold: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrBookingState
	}
	return nil
}

func (r *BookingRepository) loadSeats(ctx context.Context, b *domain.Booking) error {
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT s.id, s.row_label, s.seat_number, s.seat_class, bs.price_cents, bs.active
		FROM booking_seats bs
		JOIN seats s ON s.id = bs.seat_id
		WHERE bs.booking_id = $1::uuid
		ORDER BY s.row_label, s.seat_number`,
		b.ID,
	)
	if err != nil {
		return fmt.Errorf("load booking seats: %w", err)
	}
	defer rows.Close()

	b.Seats = b.Seats[:0]
	for rows.Next() {
		var s domain.BookedSeat
		if err := rows.Scan(&s.SeatID, &s.RowLabel, &s.SeatNumber, &s.SeatClass, &s.PriceCents, &s.Active); err != nil {
			return fmt.Errorf("scan booking seat: %w", err)
		}
		b.Seats = append(b.Seats, s)
	}
	return rows.Err()
}

func (r *BookingRepository) loadShowtime(ctx context.Context, b *domain.Booking) error {
	s, err := scanShowtime(r.db.q(ctx).QueryRow(ctx, `
		SELECT s.id, s.movie_id, m.title, s.hall_id, h.name,
		       s.starts_at, s.base_price_cents, s.currency, s.status
		FROM showtimes s
		JOIN movies m ON m.id = s.movie_id
		JOIN halls  h ON h.id = s.hall_id
		WHERE s.id = $1::uuid`, b.ShowtimeID))
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load booking showtime: %w", err)
	}
	b.Showtime = &s
	return nil
}

func scanBooking(row scanner) (domain.Booking, error) {
	var b domain.Booking
	err := row.Scan(
		&b.ID, &b.Reference, &b.ShowtimeID, &b.UserID, &b.Status, &b.TotalAmountCents,
		&b.Currency, &b.HoldExpiresAt, &b.IdempotencyKey,
		&b.ConfirmedAt, &b.CancelledAt, &b.CreatedAt, &b.UpdatedAt,
	)
	return b, err
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// referenceAlphabet omits characters that are easy to misread aloud or in
// print (0/O, 1/I) since booking references get read out at the counter.
const referenceAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// newBookingReference produces a short human-quotable booking reference.
func newBookingReference() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate booking reference: %w", err)
	}
	out := make([]byte, len(buf))
	for i, b := range buf {
		out[i] = referenceAlphabet[int(b)%len(referenceAlphabet)]
	}
	return "CB-" + string(out), nil
}
