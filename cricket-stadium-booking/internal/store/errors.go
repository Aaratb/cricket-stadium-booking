package store

import "errors"

// Sentinel errors produced by the CAS operations below. httpapi maps these
// to status codes via the booking package's thin re-exports (see
// internal/booking/errors.go) — see design/api-contract.md for the table.
var (
	ErrSeatUnavailable = errors.New("seat unavailable")
	ErrHoldExpired     = errors.New("hold expired or already resolved")
	ErrNotFound        = errors.New("not found")
	// ErrIdempotencyKeyReuse: an Idempotency-Key was presented with different
	// request parameters (match/seat/buyer) than the request that first used
	// it. Standard Idempotency-Key semantics reject this as a client error —
	// silently replaying the original booking would hand one request another
	// request's data.
	ErrIdempotencyKeyReuse = errors.New("idempotency key reused with different request parameters")
)

const uniqueViolationCode = "23505"
