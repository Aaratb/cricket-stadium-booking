package httpapi

import (
	"net/http"
	"time"

	"stadiumbooking/internal/observability"
)

func (s *Server) handleHold(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	matchID, seatID := r.PathValue("matchId"), r.PathValue("seatId")

	var req buyerRequest
	if err := decodeJSON(w, r, &req); err != nil || !validBuyerID(req.BuyerID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "buyer_id required"})
		return
	}

	// Optional idempotency: an absent header is the empty string, which the
	// service treats exactly as the non-idempotent path (unchanged behavior).
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if !validIdempotencyKey(idempotencyKey) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid idempotency key"})
		return
	}

	hold, err := s.service.PlaceHoldWithKey(r.Context(), matchID, seatID, req.BuyerID, idempotencyKey)
	observability.LogTransition(r.Context(), "hold", matchID, seatID, start, err)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, hold)
}

func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	holdID, buyerID, ok := parseIDAndBuyer(w, r, "holdId")
	if !ok {
		return
	}

	booking, err := s.service.ConfirmHold(r.Context(), holdID, buyerID)
	observability.LogTransition(r.Context(), "confirm", booking.MatchID, booking.SeatID, start, err)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, booking)
}

func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	holdID, buyerID, ok := parseIDAndBuyer(w, r, "holdId")
	if !ok {
		return
	}

	if err := s.service.ReleaseHold(r.Context(), holdID, buyerID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
