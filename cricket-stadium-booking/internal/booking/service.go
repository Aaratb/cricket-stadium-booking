package booking

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"stadiumbooking/internal/store"
)

const (
	serializationFailure = "40001"
	deadlockDetected     = "40P01"

	// Backoff bounds. baseBackoff is the first step; maxBackoff caps the
	// exponential growth so a large (or misconfigured) MAX_RETRIES can never
	// produce a multi-minute sleep — nor overflow int64 via 1<<attempt.
	baseBackoff = 10 * time.Millisecond
	maxBackoff  = 1 * time.Second
	// maxBackoffShift bounds the exponent independently of attempt, so the
	// shift itself can never overflow regardless of how large maxRetries is.
	maxBackoffShift = 20
)

type Service struct {
	store          *store.Store
	holdTTL        time.Duration
	requestTimeout time.Duration
	maxRetries     int
	seatCache      *seatCache
}

func NewService(s *store.Store, holdTTL, requestTimeout time.Duration, maxRetries int) *Service {
	return &Service{
		store:          s,
		holdTTL:        holdTTL,
		requestTimeout: requestTimeout,
		maxRetries:     maxRetries,
		seatCache:      newSeatCache(seatCacheTTL),
	}
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
	if maxRetries < 0 {
		// Defensive: a negative bound would otherwise skip the loop entirely
		// and return (zero, nil) — a silent false success without ever calling
		// fn. config.Validate() already guarantees >= 0, but withRetry must
		// not depend on a caller invariant it can't see.
		maxRetries = 0
	}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, retryCtxErr(err, lastErr)
		}
		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return zero, err
		}
		// Skip the backoff after the final attempt: no further fn() call
		// follows, so sleeping only burns the request's deadline budget for
		// nothing before we return lastErr.
		if attempt == maxRetries {
			break
		}
		timer := time.NewTimer(backoffFor(attempt))
		select {
		case <-ctx.Done():
			timer.Stop() // don't leak the timer when the context wins the race
			return zero, retryCtxErr(ctx.Err(), lastErr)
		case <-timer.C:
		}
	}
	return zero, lastErr
}

// backoffFor returns a "full jitter" exponential backoff in
// [0, min(baseBackoff*2^attempt, maxBackoff)]. Full jitter (a uniform draw up
// to the ceiling) is what actually de-correlates a fleet of goroutines all
// retrying after the same serialization storm — a fixed additive jitter leaves
// them waking in lockstep and re-colliding on the same hot rows. The exponent
// is clamped so the shift can neither overflow int64 nor exceed maxBackoff.
func backoffFor(attempt int) time.Duration {
	if attempt > maxBackoffShift {
		attempt = maxBackoffShift
	}
	ceiling := baseBackoff << uint(attempt)
	if ceiling <= 0 || ceiling > maxBackoff {
		ceiling = maxBackoff
	}
	// rand/v2's top-level generator is per-P and lock-free, avoiding the global
	// mutex that math/rand's global source serializes every caller through —
	// which matters precisely in the high-concurrency storm this guards.
	return time.Duration(rand.Int64N(int64(ceiling) + 1))
}

// retryCtxErr surfaces the context error (so errors.Is(err,
// context.DeadlineExceeded) still holds and the deadline contract is intact)
// while preserving the underlying retryable failure that was actually driving
// the retries — otherwise a request that times out mid-storm reports only
// "deadline exceeded" and hides the serialization/deadlock root cause from the
// observability layer.
func retryCtxErr(ctxErr, lastErr error) error {
	if lastErr == nil {
		return ctxErr
	}
	return fmt.Errorf("%w (last retryable error: %v)", ctxErr, lastErr)
}

func (svc *Service) Ping(ctx context.Context) error {
	return svc.store.Ping(ctx)
}

func (svc *Service) ListSeats(ctx context.Context, matchID string) ([]Seat, error) {
	ctx, cancel := svc.withDeadline(ctx)
	defer cancel()

	return svc.seatCache.load(ctx, matchID, func() ([]Seat, error) {
		// This query is shared by every concurrent caller collapsed into the
		// singleflight flight, so it must not run on any single request's
		// context — one client disconnecting (or arriving with a nearly-spent
		// deadline) would cancel the query and fail every healthy waiter.
		// WithoutCancel detaches from the leader's cancellation while keeping
		// its values; the fresh requestTimeout keeps the flight itself
		// bounded, preserving the hard-deadline contract.
		loadCtx, loadCancel := context.WithTimeout(context.WithoutCancel(ctx), svc.requestTimeout)
		defer loadCancel()
		rows, err := svc.store.ListSeats(loadCtx, matchID)
		if err != nil {
			return nil, err
		}
		out := make([]Seat, len(rows))
		for i, r := range rows {
			out[i] = Seat{SeatID: r.SeatID, Section: r.Section, Status: r.Status, HoldExpiresAt: r.HoldExpiresAt}
		}
		return out, nil
	})
}

func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == serializationFailure || pgErr.Code == deadlockDetected
}
