package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PostgreSQL error codes we react to.
const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
	codeDeadlockDetected    = "40P01"
	codeSerializationFail   = "40001"
)

// isNoRows reports whether err means the query matched nothing.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// uniqueViolation reports whether err is a unique-constraint violation on the
// given index. An empty constraint name matches any unique violation.
func uniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != codeUniqueViolation {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}

// isRetryable reports whether a failed transaction is worth replaying.
// Concurrent seat claims can deadlock on the seat-claim index; the caller
// retries rather than surfacing a 500.
func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == codeDeadlockDetected || pgErr.Code == codeSerializationFail
}
