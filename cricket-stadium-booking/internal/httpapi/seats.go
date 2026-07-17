package httpapi

import "net/http"

type seatsResponse struct {
	MatchID string      `json:"match_id"`
	Seats   []seatEntry `json:"seats"`
}

type seatEntry struct {
	SeatID        string  `json:"seat_id"`
	Section       string  `json:"section"`
	Status        string  `json:"status"`
	HoldExpiresAt *string `json:"hold_expires_at,omitempty"`
}

func (s *Server) handleListSeats(w http.ResponseWriter, r *http.Request) {
	matchID := r.PathValue("matchId")

	seats, err := s.service.ListSeats(r.Context(), matchID)
	if err != nil {
		writeError(w, err)
		return
	}

	resp := seatsResponse{MatchID: matchID, Seats: make([]seatEntry, len(seats))}
	for i, seat := range seats {
		e := seatEntry{SeatID: seat.SeatID, Section: seat.Section, Status: seat.Status}
		if seat.HoldExpiresAt != nil {
			ts := seat.HoldExpiresAt.Format("2006-01-02T15:04:05Z07:00")
			e.HoldExpiresAt = &ts
		}
		resp.Seats[i] = e
	}
	writeJSON(w, http.StatusOK, resp)
}
