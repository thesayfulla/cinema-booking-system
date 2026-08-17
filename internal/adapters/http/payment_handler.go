package http

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/thesayfulla/cinema-booking-system/internal/adapters/payment/mock"
	"github.com/thesayfulla/cinema-booking-system/internal/domain"
	"github.com/thesayfulla/cinema-booking-system/internal/metrics"
	"github.com/thesayfulla/cinema-booking-system/internal/usecase"
)

// PaymentHandler serves checkout and the provider webhook.
type PaymentHandler struct {
	payments *usecase.Payment
	metrics  *metrics.Collector
	log      *slog.Logger
	// mockProvider is set only when the mock gateway is configured; it backs
	// the simulated-callback endpoint used by the demo UI.
	mockProvider *mock.Provider
}

// NewPaymentHandler builds the payment handler.
func NewPaymentHandler(payments *usecase.Payment, m *metrics.Collector, log *slog.Logger, mockProvider *mock.Provider) *PaymentHandler {
	return &PaymentHandler{payments: payments, metrics: m, log: log, mockProvider: mockProvider}
}

// StartCheckout handles POST /api/v1/bookings/{bookingID}/checkout.
// It returns whatever the client needs to complete the charge at the provider.
func (h *PaymentHandler) StartCheckout(w http.ResponseWriter, r *http.Request) {
	checkout, err := h.payments.StartCheckout(r.Context(), r.PathValue("bookingID"), UserID(r.Context()))
	if err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}

	h.metrics.PaymentStatus(checkout.Payment.Status)

	resp := toPaymentResponse(checkout.Payment)
	resp.ClientSecret = checkout.Intent.ClientSecret
	resp.NextActionURL = checkout.Intent.NextActionURL
	writeJSON(w, http.StatusCreated, resp)
}

// GetPayment handles GET /api/v1/payments/{paymentID}, which the client polls
// while the provider settles the charge.
func (h *PaymentHandler) GetPayment(w http.ResponseWriter, r *http.Request) {
	payment, err := h.payments.GetPayment(r.Context(), r.PathValue("paymentID"), UserID(r.Context()))
	if err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentResponse(payment))
}

// Webhook handles POST /api/v1/payments/webhook.
//
// It is deliberately unauthenticated at the transport level: the provider
// cannot send our headers, so trust comes from the signature over the body,
// which the provider adapter verifies.
func (h *PaymentHandler) Webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeBadRequest, "could not read request body", "")
		return
	}

	headers := make(map[string]string, len(r.Header))
	for name := range r.Header {
		headers[name] = r.Header.Get(name)
	}

	if err := h.payments.HandleWebhook(r.Context(), headers, body); err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}

	// A plain 200 tells the provider to stop retrying this delivery.
	writeJSON(w, http.StatusOK, map[string]string{"status": "received"})
}

// SimulatePaymentRequest chooses the outcome of a simulated charge.
type SimulatePaymentRequest struct {
	// Outcome is "success" or "failure"; empty means success.
	Outcome string `json:"outcome"`
}

// SimulateProviderCallback handles POST /api/v1/payments/{paymentID}/simulate.
//
// It stands in for the customer completing payment on the gateway's page: it
// builds a properly signed callback and feeds it through the very same webhook
// path a real provider would use. Registered only when test endpoints are
// enabled, which configuration forbids in production.
func (h *PaymentHandler) SimulateProviderCallback(w http.ResponseWriter, r *http.Request) {
	if h.mockProvider == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "not found", "")
		return
	}

	var req SimulatePaymentRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeRequestError(w, r, h.log, err)
			return
		}
	}

	payment, err := h.payments.GetPayment(r.Context(), r.PathValue("paymentID"), UserID(r.Context()))
	if err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}

	eventType, reason := domain.EventPaymentSucceeded, ""
	if req.Outcome == "failure" {
		eventType, reason = domain.EventPaymentFailed, "simulated card decline"
	}

	body, signature, err := h.mockProvider.BuildEvent(eventType, payment.ProviderRef, reason)
	if err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}

	if err := h.payments.HandleWebhook(r.Context(), map[string]string{mock.SignatureHeader: signature}, body); err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}

	updated, err := h.payments.GetPayment(r.Context(), payment.ID, UserID(r.Context()))
	if err != nil {
		writeDomainError(w, r, h.log, err)
		return
	}

	h.metrics.PaymentStatus(updated.Status)
	writeJSON(w, http.StatusOK, toPaymentResponse(updated))
}
