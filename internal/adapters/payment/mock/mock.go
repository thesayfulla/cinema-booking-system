// Package mock implements domain.PaymentProvider without talking to a real
// gateway. It exists so the booking flow is complete and testable end to end
// today; swapping in a real gateway means adding a sibling package that
// implements the same interface and selecting it in configuration.
package mock

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
)

// SignatureHeader carries the HMAC of the webhook body, mirroring how real
// providers authenticate their callbacks.
const SignatureHeader = "X-Signature"

// Provider is an in-memory payment gateway.
type Provider struct {
	secret []byte

	mu sync.Mutex
	// intents maps idempotency key to provider ref so repeated CreateIntent
	// calls return the same intent, as a real gateway would.
	intents map[string]string
}

// New builds a mock provider. The secret signs webhook payloads.
func New(webhookSecret string) *Provider {
	return &Provider{
		secret:  []byte(webhookSecret),
		intents: make(map[string]string),
	}
}

func (p *Provider) Name() string { return "mock" }

// CreateIntent returns a pending intent. The charge completes when the client
// posts a webhook back (see Sign and BuildEvent), which is the same shape as a
// real redirect-and-callback flow.
func (p *Provider) CreateIntent(ctx context.Context, req domain.PaymentIntentRequest) (domain.PaymentIntent, error) {
	if req.AmountCents <= 0 {
		return domain.PaymentIntent{}, domain.Invalid("amount_cents", "must be greater than zero")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if ref, ok := p.intents[req.IdempotencyKey]; ok {
		return domain.PaymentIntent{
			ProviderRef:  ref,
			Status:       domain.PaymentPending,
			ClientSecret: p.clientSecret(ref),
		}, nil
	}

	ref, err := randomID("mock_pi_")
	if err != nil {
		return domain.PaymentIntent{}, err
	}
	p.intents[req.IdempotencyKey] = ref

	return domain.PaymentIntent{
		ProviderRef:  ref,
		Status:       domain.PaymentPending,
		ClientSecret: p.clientSecret(ref),
	}, nil
}

// Refund always succeeds; a real provider would call its API here.
func (p *Provider) Refund(ctx context.Context, providerRef string, amountCents int64) (domain.RefundResult, error) {
	if providerRef == "" {
		return domain.RefundResult{}, domain.Invalid("provider_ref", "is required")
	}
	ref, err := randomID("mock_re_")
	if err != nil {
		return domain.RefundResult{}, err
	}
	return domain.RefundResult{ProviderRef: ref, Status: domain.PaymentRefunded}, nil
}

func (p *Provider) Cancel(ctx context.Context, providerRef string) error {
	if providerRef == "" {
		return domain.Invalid("provider_ref", "is required")
	}
	return nil
}

// webhookPayload is the mock provider's callback body.
type webhookPayload struct {
	EventID     string `json:"event_id"`
	Type        string `json:"type"`
	ProviderRef string `json:"provider_ref"`
	Reason      string `json:"reason,omitempty"`
}

// ParseWebhook verifies the HMAC signature before decoding, so an unsigned or
// tampered callback can never confirm a booking.
func (p *Provider) ParseWebhook(ctx context.Context, headers map[string]string, body []byte) (domain.PaymentEvent, error) {
	signature := ""
	for k, v := range headers {
		if strings.EqualFold(k, SignatureHeader) {
			signature = v
			break
		}
	}

	expected := p.Sign(body)
	if signature == "" || !hmac.Equal([]byte(signature), []byte(expected)) {
		return domain.PaymentEvent{}, domain.ErrWebhookUnverified
	}

	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return domain.PaymentEvent{}, domain.Invalid("body", "malformed webhook payload")
	}
	if payload.EventID == "" || payload.ProviderRef == "" {
		return domain.PaymentEvent{}, domain.Invalid("body", "event_id and provider_ref are required")
	}

	switch payload.Type {
	case domain.EventPaymentSucceeded, domain.EventPaymentFailed,
		domain.EventPaymentCancelled, domain.EventPaymentRefunded:
	default:
		return domain.PaymentEvent{}, domain.Invalid("type", "unsupported event type "+payload.Type)
	}

	return domain.PaymentEvent{
		Provider:    p.Name(),
		EventID:     payload.EventID,
		Type:        payload.Type,
		ProviderRef: payload.ProviderRef,
		Payload: map[string]any{
			"type":         payload.Type,
			"provider_ref": payload.ProviderRef,
			"reason":       payload.Reason,
			"received_at":  time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

// Sign returns the HMAC a caller must send in SignatureHeader.
func (p *Provider) Sign(body []byte) string {
	mac := hmac.New(sha256.New, p.secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// BuildEvent renders a signed callback body for providerRef. The demo checkout
// endpoint uses it to simulate the gateway calling us back.
func (p *Provider) BuildEvent(eventType, providerRef, reason string) (body []byte, signature string, err error) {
	eventID, err := randomID("mock_evt_")
	if err != nil {
		return nil, "", err
	}

	body, err = json.Marshal(webhookPayload{
		EventID:     eventID,
		Type:        eventType,
		ProviderRef: providerRef,
		Reason:      reason,
	})
	if err != nil {
		return nil, "", fmt.Errorf("encode mock event: %w", err)
	}
	return body, p.Sign(body), nil
}

func (p *Provider) clientSecret(ref string) string {
	return ref + "_secret_" + p.Sign([]byte(ref))[:16]
}

func randomID(prefix string) (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + hex.EncodeToString(buf), nil
}
