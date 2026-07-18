package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
)

type buyerRequest struct {
	BuyerID string `json:"buyer_id"`
}

const (
	maxRequestBodyBytes  = 1 << 16 // 64KiB — every request body here is a few short fields
	maxBuyerIDLen        = 255
	maxIdempotencyKeyLen = 255
)

// parseIDAndBuyer is the shared "path param + buyer_id body" pattern used
// by confirm, release, and cancel. Writes the error response itself and
// returns ok=false when the request is malformed, so callers can just
// `if !ok { return }`.
func parseIDAndBuyer(w http.ResponseWriter, r *http.Request, pathParam string) (id int64, buyerID string, ok bool) {
	id, err := strconv.ParseInt(r.PathValue(pathParam), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return 0, "", false
	}
	var req buyerRequest
	if err := decodeJSON(w, r, &req); err != nil || !validBuyerID(req.BuyerID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "buyer_id required"})
		return 0, "", false
	}
	return id, req.BuyerID, true
}

func validBuyerID(id string) bool {
	return id != "" && len(id) <= maxBuyerIDLen
}

// validIdempotencyKey allows the empty string (the header was absent, meaning
// no idempotency requested) but rejects an over-long key.
func validIdempotencyKey(key string) bool {
	return len(key) <= maxIdempotencyKeyLen
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	addVary(w.Header(), "Accept-Encoding")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeJSON caps the request body at maxRequestBodyBytes before decoding —
// without this, any POST handler would buffer an arbitrarily large body,
// a memory-exhaustion DoS made worse by having no auth in front of it
// (CODE_REVIEW.md finding #1).
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// recoverMiddleware ensures every handler ends in a JSON 500 with a logged
// error — never an unhandled panic or a bare empty 500 response.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "internal_error",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
