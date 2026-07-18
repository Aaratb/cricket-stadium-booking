package httpapi

import (
	"net/http"

	"stadiumbooking/internal/booking"
)

type bookingsResponse struct {
	Bookings []booking.BookingSummary `json:"bookings"`
}

func (s *Server) handleListBookings(w http.ResponseWriter, r *http.Request) {
	buyerID := r.URL.Query().Get("buyer_id")
	if !validBuyerID(buyerID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "buyer_id required"})
		return
	}

	bookings, err := s.service.ListBookings(r.Context(), r.PathValue("matchId"), buyerID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bookingsResponse{Bookings: bookings})
}
