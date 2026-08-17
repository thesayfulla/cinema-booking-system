# Cinema Booking System

A cinema ticket booking service in Go: browse the catalog, hold seats for a
limited time, pay, and get a confirmed booking. Seat concurrency is settled by
the database — a partial unique index admits exactly one active claim per
(showtime, seat), so two racing requests resolve to one winner and one 409.

## Tech Stack

- Go 1.26
- PostgreSQL 14+ (pgx v5, embedded SQL migrations)
- Prometheus metrics, structured logs (`log/slog`)
- Mock payment provider with HMAC-signed webhooks

## Quick Start

### With Docker Compose (database + API)

```bash
docker compose up -d --build
```

The API waits for Postgres, applies migrations, seeds demo data, and serves on
<http://localhost:8080>.

### Locally (database in Docker, API on the host)

```bash
docker compose up -d postgres
DATABASE_URL='postgres://cinema:cinema@localhost:5432/cinema?sslmode=disable' go run ./cmd
```

Open <http://localhost:8080> for the demo UI.

## Configuration

Everything is read from the environment; defaults suit local development.

| Variable | Default | Purpose |
| --- | --- | --- |
| `APP_ENV` | `development` | `development` or `production` |
| `HTTP_PORT` | `8080` | Listen port |
| `DATABASE_URL` | `postgres://cinema:cinema@localhost:5432/cinema?sslmode=disable` | Postgres DSN |
| `DB_AUTO_MIGRATE` | `true` | Apply migrations at startup |
| `SEED_DEMO_DATA` | `true` in development | Load demo movies, halls and showtimes |
| `HOLD_TTL` | `5m` | How long seats stay held before checkout |
| `MAX_SEATS_PER_BOOKING` | `10` | Anti-scalping cap |
| `BOOKING_CUTOFF` | `10m` | Sales stop this long before a screening |
| `HOLD_SWEEP_INTERVAL` | `30s` | How often lapsed holds are reclaimed |
| `PAYMENT_PROVIDER` | `mock` | Gateway implementation |
| `PAYMENT_WEBHOOK_SECRET` | dev default | HMAC secret; required in production |
| `PAYMENT_WINDOW` | `10m` | Hold extension granted during checkout |
| `REFUND_CUTOFF` | `2h` | Self-service refunds close this long before a screening |
| `PAYMENT_TEST_ENDPOINTS` | `true` in development | Exposes the simulated-gateway route |
| `CORS_ORIGINS` | `*` | Explicit origins required in production |
| `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` | `20` / `40` | Per-IP throttle |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `json` | Logging |

Production refuses to start with demo seeding, test endpoints, wildcard CORS, or
a missing webhook secret.

## API

Authentication is stubbed: the caller identifies itself with an `X-User-Id`
header, which a real deployment would replace with a verified token.

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/api/v1/movies` | Public catalog |
| GET | `/api/v1/movies/{movieID}` | Accepts an id or a slug |
| GET | `/api/v1/movies/{movieID}/showtimes` | Upcoming screenings |
| GET | `/api/v1/showtimes` | All upcoming, `?movie_id=` filters |
| GET | `/api/v1/showtimes/{showtimeID}/seats` | Seat map with availability |
| POST | `/api/v1/bookings` | Hold seats; honours `Idempotency-Key` |
| GET | `/api/v1/bookings` | Caller's bookings |
| GET | `/api/v1/bookings/{bookingID}` | One booking |
| DELETE | `/api/v1/bookings/{bookingID}` | Release, refunding a paid booking |
| POST | `/api/v1/bookings/{bookingID}/checkout` | Start (or resume) a payment |
| GET | `/api/v1/payments/{paymentID}` | Poll payment status |
| POST | `/api/v1/payments/webhook` | Provider callback, verified by signature |
| POST | `/api/v1/payments/{paymentID}/simulate` | Development only |
| GET | `/healthz`, `/readyz`, `/metrics` | Operations |

Example: hold two seats and pay for them.

```bash
BASE=http://localhost:8080/api/v1
SHOWTIME=$(curl -s $BASE/showtimes | jq -r '.showtimes[0].id')
SEATS=$(curl -s $BASE/showtimes/$SHOWTIME/seats | jq -c '[.seats[] | select(.status=="available")][:2] | map(.id)')

BOOKING=$(curl -s -X POST $BASE/bookings \
  -H 'X-User-Id: demo' -H 'Idempotency-Key: demo-1' -H 'Content-Type: application/json' \
  -d "{\"showtime_id\":\"$SHOWTIME\",\"seat_ids\":$SEATS}" | jq -r .id)

PAYMENT=$(curl -s -X POST $BASE/bookings/$BOOKING/checkout -H 'X-User-Id: demo' | jq -r .id)
curl -s -X POST $BASE/payments/$PAYMENT/simulate -H 'X-User-Id: demo' \
  -H 'Content-Type: application/json' -d '{"outcome":"success"}'
```

## Tests

```bash
go test ./...
```

Repository tests run against a real database when `TEST_DATABASE_URL` is set and
skip otherwise.

## Layout

```
cmd/                     entrypoint and dependency wiring
internal/domain/         entities, ports, sentinel errors
internal/usecase/        booking, catalog and payment rules
internal/adapters/http/  handlers, DTOs, middleware, router
internal/adapters/postgres/  repositories, migrations, seed data
internal/adapters/payment/   mock gateway
internal/worker/         hold expiry sweeper
static/                  demo UI
```
