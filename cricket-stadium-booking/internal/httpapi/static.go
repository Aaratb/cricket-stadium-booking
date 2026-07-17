package httpapi

import (
	"net/http"

	"stadiumbooking/web"
)

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.service.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "down"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, web.FS, "index.html")
}

func (s *Server) handleAppJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	http.ServeFileFS(w, r, web.FS, "app.js")
}
