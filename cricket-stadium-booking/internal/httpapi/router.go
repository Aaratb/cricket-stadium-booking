package httpapi

import (
	"net/http"

	"stadiumbooking/internal/booking"
)

type Server struct {
	service *booking.Service
}

func NewServer(service *booking.Service, rateLimitEnabled bool) http.Handler {
	s := &Server{service: service}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /matches/{matchId}/seats/{seatId}/hold", s.handleHold)
	mux.HandleFunc("POST /holds/{holdId}/confirm", s.handleConfirm)
	mux.HandleFunc("DELETE /holds/{holdId}", s.handleRelease)
	mux.HandleFunc("GET /matches/{matchId}/seats", s.handleListSeats)
	mux.HandleFunc("GET /matches/{matchId}/bookings", s.handleListBookings)
	mux.HandleFunc("POST /bookings/{bookingId}/cancel", s.handleCancel)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /app.js", s.handleAppJS)
	mux.HandleFunc("GET /", s.handleStatic)

	var handler http.Handler = mux
	if rateLimitEnabled {
		handler = rateLimitMiddleware(handler)
	}
	return gzipMiddleware(recoverMiddleware(handler))
}
