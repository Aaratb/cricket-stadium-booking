package httpapi

import (
	"errors"
	"net/http"

	"stadiumbooking/internal/booking"
)

// writeError maps booking-layer sentinel errors to the status codes
// specified in design/api-contract.md's table — the single translation
// point so handlers never hardcode status codes.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, booking.ErrSeatUnavailable):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "seat_unavailable"})
	case errors.Is(err, booking.ErrHoldExpired):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "hold_expired"})
	case errors.Is(err, booking.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	case errors.Is(err, booking.ErrIdempotencyKeyReuse):
		// 422, matching the IETF Idempotency-Key draft: the key is
		// syntactically fine but was already used with a different request.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "idempotency_key_reuse"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
	}
}
