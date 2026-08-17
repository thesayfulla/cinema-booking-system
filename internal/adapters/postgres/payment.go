package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// PaymentRepository persists payments and provider callbacks.
type PaymentRepository struct {
	db *DB
}

// NewPaymentRepository wires a payment repository to the pool.
func NewPaymentRepository(db *DB) *PaymentRepository { return &PaymentRepository{db: db} }

const paymentColumns = `
	id, booking_id, provider, coalesce(provider_ref, ''), status, amount_cents,
	currency, idempotency_key, coalesce(failure_reason, ''), metadata,
	created_at, updated_at`

func (r *PaymentRepository) Create(ctx context.Context, p domain.NewPayment) (domain.Payment, error) {
	metadata, err := json.Marshal(orEmptyMap(p.Metadata))
	if err != nil {
		return domain.Payment{}, fmt.Errorf("encode payment metadata: %w", err)
	}

	payment, err := scanPayment(r.db.q(ctx).QueryRow(ctx, `
		INSERT INTO payments (booking_id, provider, provider_ref, status,
		                      amount_cents, currency, idempotency_key, metadata)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+paymentColumns,
		p.BookingID, p.Provider, nullableString(p.ProviderRef), p.Status,
		p.AmountCents, p.Currency, p.IdempotencyKey, metadata))
	switch {
	case uniqueViolation(err, "payments_single_open_idx"):
		return domain.Payment{}, domain.ErrPaymentInProgress
	case uniqueViolation(err, "payments_idempotency_idx"):
		// Same checkout replayed: return the payment the first call created.
		return r.getByIdempotencyKey(ctx, p.IdempotencyKey)
	case err != nil:
		return domain.Payment{}, fmt.Errorf("insert payment: %w", err)
	}
	return payment, nil
}

func (r *PaymentRepository) GetByID(ctx context.Context, paymentID string) (domain.Payment, error) {
	if !isUUID(paymentID) {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	return r.one(ctx, `SELECT `+paymentColumns+` FROM payments WHERE id = $1::uuid`, paymentID)
}

func (r *PaymentRepository) GetByProviderRef(ctx context.Context, provider, ref string) (domain.Payment, error) {
	if ref == "" {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	return r.one(ctx,
		`SELECT `+paymentColumns+` FROM payments WHERE provider = $1 AND provider_ref = $2`,
		provider, ref)
}

func (r *PaymentRepository) GetOpenByBooking(ctx context.Context, bookingID string) (domain.Payment, error) {
	if !isUUID(bookingID) {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	return r.one(ctx, `SELECT `+paymentColumns+` FROM payments
		WHERE booking_id = $1::uuid AND status IN ('pending', 'processing')`, bookingID)
}

func (r *PaymentRepository) GetSucceededByBooking(ctx context.Context, bookingID string) (domain.Payment, error) {
	if !isUUID(bookingID) {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	return r.one(ctx, `SELECT `+paymentColumns+` FROM payments
		WHERE booking_id = $1::uuid AND status = 'succeeded'
		ORDER BY created_at DESC LIMIT 1`, bookingID)
}

func (r *PaymentRepository) ListByBooking(ctx context.Context, bookingID string) ([]domain.Payment, error) {
	if !isUUID(bookingID) {
		return nil, domain.ErrPaymentNotFound
	}

	rows, err := r.db.q(ctx).Query(ctx,
		`SELECT `+paymentColumns+` FROM payments WHERE booking_id = $1::uuid ORDER BY created_at`, bookingID)
	if err != nil {
		return nil, fmt.Errorf("list payments: %w", err)
	}
	defer rows.Close()

	payments := make([]domain.Payment, 0, 4)
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan payment: %w", err)
		}
		payments = append(payments, p)
	}
	return payments, rows.Err()
}

// Update applies a status transition. Terminal statuses are never overwritten,
// so an out-of-order provider callback cannot resurrect a settled payment.
func (r *PaymentRepository) Update(ctx context.Context, paymentID string, u domain.PaymentUpdate) (domain.Payment, error) {
	payment, err := scanPayment(r.db.q(ctx).QueryRow(ctx, `
		UPDATE payments
		SET status         = $2,
		    provider_ref   = coalesce(nullif($3, ''), provider_ref),
		    failure_reason = nullif($4, ''),
		    updated_at     = now()
		WHERE id = $1::uuid
		  AND (status IN ('pending', 'processing') OR (status = 'succeeded' AND $2 = 'refunded'))
		RETURNING `+paymentColumns,
		paymentID, u.Status, u.ProviderRef, u.FailureReason))
	if isNoRows(err) {
		return domain.Payment{}, domain.ErrPaymentState
	}
	if err != nil {
		return domain.Payment{}, fmt.Errorf("update payment: %w", err)
	}
	return payment, nil
}

// RecordEvent stores a provider callback exactly once. The false return tells
// the caller this delivery is a duplicate and must not be acted on again.
func (r *PaymentRepository) RecordEvent(ctx context.Context, e domain.PaymentEvent) (bool, error) {
	payload, err := json.Marshal(orEmptyMap(e.Payload))
	if err != nil {
		return false, fmt.Errorf("encode event payload: %w", err)
	}

	tag, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO payment_events (payment_id, provider, provider_event_id, event_type, payload)
		VALUES ((SELECT id FROM payments WHERE provider = $1 AND provider_ref = $5),
		        $1, $2, $3, $4)
		ON CONFLICT (provider, provider_event_id) DO NOTHING`,
		e.Provider, e.EventID, e.Type, payload, e.ProviderRef)
	if err != nil {
		return false, fmt.Errorf("record payment event: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *PaymentRepository) getByIdempotencyKey(ctx context.Context, key string) (domain.Payment, error) {
	return r.one(ctx, `SELECT `+paymentColumns+` FROM payments WHERE idempotency_key = $1`, key)
}

func (r *PaymentRepository) one(ctx context.Context, sql string, args ...any) (domain.Payment, error) {
	p, err := scanPayment(r.db.q(ctx).QueryRow(ctx, sql, args...))
	if isNoRows(err) {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	if err != nil {
		return domain.Payment{}, fmt.Errorf("get payment: %w", err)
	}
	return p, nil
}

func scanPayment(row scanner) (domain.Payment, error) {
	var (
		p        domain.Payment
		metadata []byte
	)
	if err := row.Scan(
		&p.ID, &p.BookingID, &p.Provider, &p.ProviderRef, &p.Status, &p.AmountCents,
		&p.Currency, &p.IdempotencyKey, &p.FailureReason, &metadata,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return domain.Payment{}, err
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &p.Metadata); err != nil {
			return domain.Payment{}, fmt.Errorf("decode payment metadata: %w", err)
		}
	}
	return p, nil
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
