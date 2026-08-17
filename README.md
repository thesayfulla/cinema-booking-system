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

### Docker Compose

```bash
docker compose up -d --build
```

The API waits for Postgres, applies migrations, seeds demo data, and serves on
<http://localhost:8080>.

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
