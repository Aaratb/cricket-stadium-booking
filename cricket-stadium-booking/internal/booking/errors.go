package booking

import "stadiumbooking/internal/store"

// Re-exported so httpapi depends only on the booking package, not store
// directly, keeping the layer boundary from spec.md intact.
var (
	ErrSeatUnavailable     = store.ErrSeatUnavailable
	ErrHoldExpired         = store.ErrHoldExpired
	ErrNotFound            = store.ErrNotFound
	ErrIdempotencyKeyReuse = store.ErrIdempotencyKeyReuse
)
