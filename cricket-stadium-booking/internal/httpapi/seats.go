package httpapi

import (
	"fmt"
	"net/http"
)

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

	seats, version, err := s.service.ListSeatsVersioned(r.Context(), matchID)
	if err != nil {
		writeError(w, err)
		return
	}

	// Browsers revalidate this public snapshot on each application-driven
	// poll. An unchanged map becomes a header-only 304, while cache
	// invalidation after every mutation guarantees immediate read-your-writes.
	etag := fmt.Sprintf(`W/"seats-%s"`, version)
	w.Header().Set("Cache-Control", "public, no-cache")
	w.Header().Set("ETag", etag)
	addVary(w.Header(), "Accept-Encoding")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
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
