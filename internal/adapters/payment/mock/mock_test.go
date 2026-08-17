package mock_test

import (
	"context"
	"errors"
	"testing"

	"github.com/thesayfulla/cinema-booking-system/internal/adapters/payment/mock"
	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

func TestCreateIntentIsIdempotent(t *testing.T) {
	p := mock.New("secret")
	ctx := context.Background()

	req := domain.PaymentIntentRequest{
		BookingID:      "booking-1",
		AmountCents:    2500,
		Currency:       "USD",
		IdempotencyKey: "booking:booking-1",
	}

	first, err := p.CreateIntent(ctx, req)
	if err != nil {
		t.Fatalf("CreateIntent: %v", err)
	}
	second, err := p.CreateIntent(ctx, req)
	if err != nil {
		t.Fatalf("repeat CreateIntent: %v", err)
	}

	if first.ProviderRef != second.ProviderRef {
		t.Errorf("refs differ: %s vs %s", first.ProviderRef, second.ProviderRef)
	}
	if first.Status != domain.PaymentPending {
		t.Errorf("status = %q, want %q", first.Status, domain.PaymentPending)
	}
}

func TestParseWebhookRequiresValidSignature(t *testing.T) {
	p := mock.New("secret")
	ctx := context.Background()

	body, signature, err := p.BuildEvent(domain.EventPaymentSucceeded, "mock_pi_1", "")
	if err != nil {
		t.Fatalf("BuildEvent: %v", err)
	}

	event, err := p.ParseWebhook(ctx, map[string]string{mock.SignatureHeader: signature}, body)
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if event.Type != domain.EventPaymentSucceeded || event.ProviderRef != "mock_pi_1" {
		t.Errorf("unexpected event: %+v", event)
	}
	if event.EventID == "" {
		t.Error("expected an event id for deduplication")
	}

	t.Run("wrong signature", func(t *testing.T) {
		_, err := p.ParseWebhook(ctx, map[string]string{mock.SignatureHeader: "deadbeef"}, body)
		if !errors.Is(err, domain.ErrWebhookUnverified) {
			t.Fatalf("err = %v, want ErrWebhookUnverified", err)
		}
	})

	t.Run("missing signature", func(t *testing.T) {
		if _, err := p.ParseWebhook(ctx, nil, body); !errors.Is(err, domain.ErrWebhookUnverified) {
			t.Fatalf("err = %v, want ErrWebhookUnverified", err)
		}
	})

	t.Run("tampered body", func(t *testing.T) {
		tampered := append([]byte(nil), body...)
		tampered[len(tampered)-2] ^= 0xFF
		if _, err := p.ParseWebhook(ctx, map[string]string{mock.SignatureHeader: signature}, tampered); !errors.Is(err, domain.ErrWebhookUnverified) {
			t.Fatalf("err = %v, want ErrWebhookUnverified", err)
		}
	})

	t.Run("signed by another secret", func(t *testing.T) {
		other := mock.New("different-secret")
		otherBody, otherSig, err := other.BuildEvent(domain.EventPaymentSucceeded, "mock_pi_1", "")
		if err != nil {
			t.Fatalf("BuildEvent: %v", err)
		}
		if _, err := p.ParseWebhook(ctx, map[string]string{mock.SignatureHeader: otherSig}, otherBody); !errors.Is(err, domain.ErrWebhookUnverified) {
			t.Fatalf("err = %v, want ErrWebhookUnverified", err)
		}
	})
}

func TestParseWebhookRejectsUnknownEventType(t *testing.T) {
	p := mock.New("secret")

	body := []byte(`{"event_id":"e1","type":"payment.exploded","provider_ref":"mock_pi_1"}`)
	_, err := p.ParseWebhook(context.Background(), map[string]string{mock.SignatureHeader: p.Sign(body)}, body)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("err = %v, want a validation error", err)
	}
}
