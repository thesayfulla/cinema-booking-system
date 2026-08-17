package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/thesayfulla/cinema-booking-system/internal/domain"
	"github.com/thesayfulla/cinema-booking-system/internal/logger"
)

// ErrorResponse is the single error shape the API returns.
type ErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Field   string `json:"field,omitempty"`
	} `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

// Machine-readable error codes. Clients should branch on these, not on prose.
const (
	codeValidation     = "validation_error"
	codeNotFound       = "not_found"
	codeConflict       = "conflict"
	codeSeatTaken      = "seat_unavailable"
	codeExpired        = "expired"
	codeForbidden      = "forbidden"
	codeUnauthorized   = "unauthorized"
	codeRateLimited    = "rate_limited"
	codeProviderDown   = "payment_provider_unavailable"
	codeInternal       = "internal_error"
	codeBadRequest     = "bad_request"
	codePayloadTooBig  = "payload_too_large"
	codeUnsupportedFmt = "unsupported_media_type"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent, so this can only be logged.
		slog.Default().Error("failed to encode response", "error", err)
	}
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message, field string) {
	var resp ErrorResponse
	resp.Error.Code = code
	resp.Error.Message = message
	resp.Error.Field = field
	resp.RequestID = logger.RequestID(r.Context())
	writeJSON(w, status, resp)
}

// writeDomainError maps a domain or use case error onto an HTTP response.
// Keeping the mapping in one place is what lets the layers below stay free of
// HTTP concepts.
func writeDomainError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var validation *domain.ValidationError
	if errors.As(err, &validation) {
		writeError(w, r, http.StatusBadRequest, codeValidation, validation.Message, validation.Field)
		return
	}

	switch {
	case errors.Is(err, domain.ErrValidation):
		writeError(w, r, http.StatusBadRequest, codeValidation, err.Error(), "")

	case errors.Is(err, domain.ErrMovieNotFound),
		errors.Is(err, domain.ErrShowtimeNotFound),
		errors.Is(err, domain.ErrSeatNotFound),
		errors.Is(err, domain.ErrBookingNotFound),
		errors.Is(err, domain.ErrPaymentNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, err.Error(), "")

	case errors.Is(err, domain.ErrSeatUnavailable):
		// 409: the request was valid, someone else simply got there first.
		writeError(w, r, http.StatusConflict, codeSeatTaken, err.Error(), "")

	case errors.Is(err, domain.ErrPaymentInProgress):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error(), "")

	case errors.Is(err, domain.ErrBookingExpired):
		writeError(w, r, http.StatusGone, codeExpired, err.Error(), "")

	case errors.Is(err, domain.ErrBookingState),
		errors.Is(err, domain.ErrPaymentState),
		errors.Is(err, domain.ErrShowtimeStarted),
		errors.Is(err, domain.ErrShowtimeCanceled):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error(), "")

	case errors.Is(err, domain.ErrUnauthorized):
		writeError(w, r, http.StatusForbidden, codeForbidden, err.Error(), "")

	case errors.Is(err, domain.ErrWebhookUnverified):
		writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "signature verification failed", "")

	case errors.Is(err, domain.ErrProviderUnavailable):
		log.ErrorContext(r.Context(), "payment provider error", "error", err)
		writeError(w, r, http.StatusBadGateway, codeProviderDown,
			"the payment provider is unavailable, please try again", "")

	default:
		// Unexpected: log the detail, tell the client nothing that could leak
		// internals, and let them quote the request id to support.
		log.ErrorContext(r.Context(), "unhandled error", "error", err,
			"method", r.Method, "path", r.URL.Path)
		writeError(w, r, http.StatusInternalServerError, codeInternal,
			"an unexpected error occurred", "")
	}
}

// decodeJSON reads a JSON body and reports client mistakes as clean errors.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !isJSONContentType(ct) {
		return &httpError{status: http.StatusUnsupportedMediaType, code: codeUnsupportedFmt,
			message: "Content-Type must be application/json"}
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return &httpError{status: http.StatusRequestEntityTooLarge, code: codePayloadTooBig,
				message: "request body is too large"}
		}
		return &httpError{status: http.StatusBadRequest, code: codeBadRequest,
			message: "malformed JSON body"}
	}
	if dec.More() {
		return &httpError{status: http.StatusBadRequest, code: codeBadRequest,
			message: "body must contain a single JSON object"}
	}
	return nil
}

// httpError is a transport-level problem detected before the use case runs.
type httpError struct {
	status  int
	code    string
	message string
}

func (e *httpError) Error() string { return e.message }

// writeRequestError renders an httpError, falling back to the domain mapping.
func writeRequestError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var he *httpError
	if errors.As(err, &he) {
		writeError(w, r, he.status, he.code, he.message, "")
		return
	}
	writeDomainError(w, r, log, err)
}

func isJSONContentType(ct string) bool {
	for _, prefix := range []string{"application/json", "application/vnd.api+json"} {
		if len(ct) >= len(prefix) && ct[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
