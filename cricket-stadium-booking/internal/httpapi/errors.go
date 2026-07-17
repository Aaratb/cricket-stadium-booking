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
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
	}
}
