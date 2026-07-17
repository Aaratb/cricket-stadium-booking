package booking

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func retryableErr() error {
	return &pgconn.PgError{Code: serializationFailure}
}

func TestWithRetry_SucceedsFirstTry(t *testing.T) {
	calls := 0
	result, err := withRetry(context.Background(), 3, func() (int, error) {
		calls++
		return 42, nil
	})
	if err != nil || result != 42 || calls != 1 {
		t.Errorf("got result=%d err=%v calls=%d, want 42/nil/1", result, err, calls)
	}
}

func TestWithRetry_RetriesOnRetryableErrorThenSucceeds(t *testing.T) {
	calls := 0
	result, err := withRetry(context.Background(), 3, func() (int, error) {
		calls++
		if calls < 3 {
			return 0, retryableErr()
		}
		return 7, nil
	})
	if err != nil || result != 7 || calls != 3 {
		t.Errorf("got result=%d err=%v calls=%d, want 7/nil/3", result, err, calls)
	}
}

func TestWithRetry_BoundedByMaxRetries(t *testing.T) {
	calls := 0
	_, err := withRetry(context.Background(), 3, func() (int, error) {
		calls++
		return 0, retryableErr()
	})
	// maxRetries=3 means attempts 0,1,2,3 -> 4 total calls, then give up.
	// The point of this test: it terminates at all, and at the documented
	// bound -- an unbounded retry loop was exactly the failure mode
	// customer-pain-points.md flagged (a retry storm feeding on itself).
	if calls != 4 {
		t.Errorf("calls = %d, want 4 (bounded, not unbounded)", calls)
	}
	if err == nil {
		t.Error("err is nil, want the last retryable error surfaced")
	}
}

func TestWithRetry_NonRetryableErrorFailsFast(t *testing.T) {
	calls := 0
	sentinel := errors.New("not a serialization failure")
	_, err := withRetry(context.Background(), 3, func() (int, error) {
		calls++
		return 0, sentinel
	})
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (non-retryable errors must not retry)", calls)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

// TestWithRetry_RespectsContextCancellation is the regression test for
// CODE_REVIEW.md finding #2: withRetry's backoff sleep must select on
// ctx.Done(), not just time.Sleep blindly -- otherwise a request can retry
// well past its own deadline during a sustained serialization-error storm.
func TestWithRetry_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := withRetry(ctx, 10, func() (int, error) {
		return 0, retryableErr() // always retryable -> would retry all 10 times without the ctx check
	})
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	// 10 retries at exponential backoff (10ms, 20ms, 40ms...) would take
	// well over a second if the deadline weren't honored. Bounding this
	// at 200ms proves the ctx.Done() select actually cuts it short.
	if elapsed > 200*time.Millisecond {
		t.Errorf("elapsed = %v, want well under 200ms (backoff should have been cut short by ctx cancellation)", elapsed)
	}
}
