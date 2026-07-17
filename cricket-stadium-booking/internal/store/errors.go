package store

import "errors"

// Sentinel errors produced by the CAS operations below. httpapi maps these
// to status codes via the booking package's thin re-exports (see
// internal/booking/errors.go) — see design/api-contract.md for the table.
var (
	ErrSeatUnavailable = errors.New("seat unavailable")
	ErrHoldExpired     = errors.New("hold expired or already resolved")
	ErrNotFound        = errors.New("not found")
)

const uniqueViolationCode = "23505"
