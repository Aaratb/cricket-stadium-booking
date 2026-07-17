package booking

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"stadiumbooking/internal/store"
)

const (
	serializationFailure = "40001"
	deadlockDetected     = "40P01"
)

type Service struct {
	store          *store.Store
	holdTTL        time.Duration
	requestTimeout time.Duration
	maxRetries     int
}

func NewService(s *store.Store, holdTTL, requestTimeout time.Duration, maxRetries int) *Service {
	return &Service{store: s, holdTTL: holdTTL, requestTimeout: requestTimeout, maxRetries: maxRetries}
}

// withDeadline enforces the hard per-request deadline from
// customer-pain-points.md item 1: a goroutine blocked on a saturated pool
// must return an error at the deadline, never hang indefinitely.
func (svc *Service) withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, svc.requestTimeout)
}

// withRetry bounds retries on transient serialization/deadlock errors
// (customer-pain-points.md item 2) — an unbounded retry loop under
// sustained overload is a self-feeding failure mode, not a mitigation.
//
// The backoff sleep selects on ctx.Done() (CODE_REVIEW.md finding #2): a
// plain time.Sleep here would let a request retry past its own deadline
// during a serialization-error storm, silently defeating the "hard
// deadline, never hang" guarantee withDeadline is supposed to provide.
func withRetry[T any](ctx context.Context, maxRetries int, fn func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return zero, err
		}
		backoff := time.Duration(1<<attempt)*10*time.Millisecond + time.Duration(rand.Intn(10))*time.Millisecond
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return zero, lastErr
}

func (svc *Service) Ping(ctx context.Context) error {
	return svc.store.Ping(ctx)
}

func (svc *Service) ListSeats(ctx context.Context, matchID string) ([]Seat, error) {
	ctx, cancel := svc.withDeadline(ctx)
	defer cancel()

	rows, err := svc.store.ListSeats(ctx, matchID)
	if err != nil {
		return nil, err
	}
	out := make([]Seat, len(rows))
	for i, r := range rows {
		out[i] = Seat{SeatID: r.SeatID, Section: r.Section, Status: r.Status, HoldExpiresAt: r.HoldExpiresAt}
	}
	return out, nil
}

func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == serializationFailure || pgErr.Code == deadlockDetected
}
