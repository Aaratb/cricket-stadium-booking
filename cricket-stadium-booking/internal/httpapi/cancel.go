package httpapi

import "net/http"

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	bookingID, buyerID, ok := parseIDAndBuyer(w, r, "bookingId")
	if !ok {
		return
	}

	booking, err := s.service.CancelBooking(r.Context(), bookingID, buyerID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, booking)
}
