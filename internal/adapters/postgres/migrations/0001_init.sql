-- Core catalog: halls and their physical seats.

CREATE TABLE halls (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE seats (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    hall_id     uuid        NOT NULL REFERENCES halls (id) ON DELETE CASCADE,
    row_label   text        NOT NULL,
    seat_number int         NOT NULL CHECK (seat_number > 0),
    seat_class  text        NOT NULL DEFAULT 'standard'
                            CHECK (seat_class IN ('standard', 'premium', 'accessible')),
    UNIQUE (hall_id, row_label, seat_number)
);

CREATE INDEX seats_hall_idx ON seats (hall_id);

CREATE TABLE movies (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug             text        NOT NULL UNIQUE,
    title            text        NOT NULL,
    description      text        NOT NULL DEFAULT '',
    duration_minutes int         NOT NULL CHECK (duration_minutes > 0),
    poster_url       text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE showtimes (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    movie_id         uuid        NOT NULL REFERENCES movies (id) ON DELETE CASCADE,
    hall_id          uuid        NOT NULL REFERENCES halls (id) ON DELETE RESTRICT,
    starts_at        timestamptz NOT NULL,
    base_price_cents bigint      NOT NULL CHECK (base_price_cents >= 0),
    currency         char(3)     NOT NULL DEFAULT 'USD',
    status           text        NOT NULL DEFAULT 'scheduled'
                                 CHECK (status IN ('scheduled', 'cancelled')),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    -- A hall cannot host two screenings at the same moment.
    UNIQUE (hall_id, starts_at)
);

CREATE INDEX showtimes_movie_starts_idx ON showtimes (movie_id, starts_at);

-- Bookings: a booking groups one or more seats for a single showtime.

CREATE TABLE bookings (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    reference          text        NOT NULL UNIQUE,
    showtime_id        uuid        NOT NULL REFERENCES showtimes (id) ON DELETE RESTRICT,
    user_id            text        NOT NULL,
    status             text        NOT NULL
                                   CHECK (status IN ('held', 'confirmed', 'cancelled', 'expired')),
    total_amount_cents bigint      NOT NULL CHECK (total_amount_cents >= 0),
    currency           char(3)     NOT NULL,
    -- Set while the booking is held; cleared once it reaches a terminal state.
    hold_expires_at    timestamptz,
    idempotency_key    text,
    confirmed_at       timestamptz,
    cancelled_at       timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

-- Replaying the same create-booking request must not create a second booking.
CREATE UNIQUE INDEX bookings_idempotency_idx
    ON bookings (user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX bookings_showtime_status_idx ON bookings (showtime_id, status);
CREATE INDEX bookings_user_idx ON bookings (user_id, created_at DESC);
-- Drives the hold expiry sweeper; only held rows can expire.
CREATE INDEX bookings_expiry_idx ON bookings (hold_expires_at) WHERE status = 'held';

CREATE TABLE booking_seats (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_id  uuid   NOT NULL REFERENCES bookings (id) ON DELETE CASCADE,
    showtime_id uuid   NOT NULL REFERENCES showtimes (id) ON DELETE RESTRICT,
    seat_id     uuid   NOT NULL REFERENCES seats (id) ON DELETE RESTRICT,
    price_cents bigint NOT NULL CHECK (price_cents >= 0),
    -- False once the owning booking is cancelled or expired, which frees the seat
    -- while keeping the historical row for auditing.
    active      boolean NOT NULL DEFAULT true,
    UNIQUE (booking_id, seat_id)
);

-- The concurrency guarantee of the whole system: a seat can have at most one
-- active claim per showtime. Two racing holds resolve to one winner and one
-- unique-violation, with no application-level locking involved.
CREATE UNIQUE INDEX booking_seats_one_active_claim_idx
    ON booking_seats (showtime_id, seat_id)
    WHERE active;

CREATE INDEX booking_seats_booking_idx ON booking_seats (booking_id);

-- Payments: a booking is paid by at most one open payment at a time.

CREATE TABLE payments (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_id      uuid        NOT NULL REFERENCES bookings (id) ON DELETE RESTRICT,
    provider        text        NOT NULL,
    -- Identifier assigned by the payment provider (e.g. a Stripe PaymentIntent id).
    provider_ref    text,
    status          text        NOT NULL
                                CHECK (status IN ('pending', 'processing', 'succeeded',
                                                  'failed', 'cancelled', 'refunded')),
    amount_cents    bigint      NOT NULL CHECK (amount_cents >= 0),
    currency        char(3)     NOT NULL,
    idempotency_key text        NOT NULL,
    failure_reason  text,
    metadata        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX payments_idempotency_idx ON payments (idempotency_key);
CREATE UNIQUE INDEX payments_provider_ref_idx
    ON payments (provider, provider_ref)
    WHERE provider_ref IS NOT NULL;
CREATE INDEX payments_booking_idx ON payments (booking_id);
-- At most one in-flight payment per booking, so a double-click cannot charge twice.
CREATE UNIQUE INDEX payments_single_open_idx
    ON payments (booking_id)
    WHERE status IN ('pending', 'processing');

-- Every provider callback is recorded once; the unique key makes webhook
-- delivery retries (which every provider does) safe to replay.
CREATE TABLE payment_events (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id        uuid        REFERENCES payments (id) ON DELETE SET NULL,
    provider          text        NOT NULL,
    provider_event_id text        NOT NULL,
    event_type        text        NOT NULL,
    payload           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    received_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_event_id)
);

CREATE INDEX payment_events_payment_idx ON payment_events (payment_id);
