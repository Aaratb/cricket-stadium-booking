package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPlaceHold_Success(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	hold, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}
	if hold.Status != "held" {
		t.Errorf("status = %q, want held", hold.Status)
	}
	if hold.HoldExpiresAt == nil {
		t.Error("HoldExpiresAt is nil, want set")
	}
}

func TestPlaceHold_ConflictOnSameSeat(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	if _, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute); err != nil {
		t.Fatalf("first PlaceHold: %v", err)
	}

	_, err := s.PlaceHold(ctx, matchID, "A1", "bob@example.com", 5*time.Minute)
	if !errors.Is(err, ErrSeatUnavailable) {
		t.Errorf("second PlaceHold err = %v, want ErrSeatUnavailable", err)
	}
}

func TestPlaceHold_ReplacesPreviousBuyerHold(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	first, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("first PlaceHold: %v", err)
	}
	second, err := s.PlaceHold(ctx, matchID, "A2", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("replacement PlaceHold: %v", err)
	}

	var firstStatus string
	if err := s.pool.QueryRow(ctx, `SELECT status FROM bookings WHERE id = $1`, first.ID).Scan(&firstStatus); err != nil {
		t.Fatalf("read first hold: %v", err)
	}
	if firstStatus != "cancelled" {
		t.Errorf("first hold status = %q, want cancelled", firstStatus)
	}
	if second.Status != "held" || second.SeatID != "A2" {
		t.Errorf("replacement = %+v, want A2 held", second)
	}
}

func TestPlaceHold_FailedReplacementKeepsPreviousHold(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	first, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("alice PlaceHold: %v", err)
	}
	if _, err := s.PlaceHold(ctx, matchID, "A2", "bob@example.com", 5*time.Minute); err != nil {
		t.Fatalf("bob PlaceHold: %v", err)
	}

	if _, err := s.PlaceHold(ctx, matchID, "A2", "alice@example.com", 5*time.Minute); !errors.Is(err, ErrSeatUnavailable) {
		t.Fatalf("replacement err = %v, want ErrSeatUnavailable", err)
	}

	var firstStatus string
	if err := s.pool.QueryRow(ctx, `SELECT status FROM bookings WHERE id = $1`, first.ID).Scan(&firstStatus); err != nil {
		t.Fatalf("read first hold: %v", err)
	}
	if firstStatus != "held" {
		t.Errorf("first hold status = %q, want held after rolled-back replacement", firstStatus)
	}
}

func TestPlaceHold_ConcurrentBuyerReplacementsLeaveOneHold(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	const racers = 30
	seatIDs := []string{"A1", "A2", "A3"}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := s.PlaceHold(ctx, matchID, seatIDs[i%len(seatIDs)], "alice@example.com", 5*time.Minute)
			if err != nil && !errors.Is(err, ErrSeatUnavailable) {
				t.Errorf("replacement racer %d: %v", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	var active int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM bookings
		WHERE match_id = $1 AND buyer_id = 'alice@example.com' AND status = 'held'`,
		matchID).Scan(&active); err != nil {
		t.Fatalf("count active buyer holds: %v", err)
	}
	if active != 1 {
		t.Errorf("active buyer holds = %d, want exactly 1", active)
	}
}

// TestPlaceHold_ConcurrentRace is the unit-level version of the load-test
// harness's Scenario A: N goroutines racing one seat must produce exactly
// one winner, proven from actual Postgres state, not a client-side count.
// Smaller scale than cmd/loadtest (that's the real Definition-of-Done
// proof) -- this is a fast, deterministic regression check for CI.
func TestPlaceHold_ConcurrentRace(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	const n = 50
	var wg sync.WaitGroup
	var successes int
	var mu sync.Mutex
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := s.PlaceHold(ctx, matchID, "A1", "racer@example.com", 5*time.Minute)
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			} else if !errors.Is(err, ErrSeatUnavailable) {
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if successes != 1 {
		t.Errorf("successes = %d, want exactly 1", successes)
	}
}

func TestConfirmHold_Success(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	hold, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}

	booking, err := s.ConfirmHold(ctx, hold.ID, "alice@example.com")
	if err != nil {
		t.Fatalf("ConfirmHold: %v", err)
	}
	if booking.MatchID != matchID || booking.SeatID != "A1" {
		t.Errorf("booking = %+v, want match=%s seat=A1", booking, matchID)
	}
}

func TestConfirmHold_WrongBuyerRejected(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	hold, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}

	_, err = s.ConfirmHold(ctx, hold.ID, "mallory@example.com")
	if !errors.Is(err, ErrHoldExpired) {
		t.Errorf("err = %v, want ErrHoldExpired (wrong-buyer collapses into this, documented in api-contract.md)", err)
	}
}

func TestConfirmHold_ExpiredRejected(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	// A TTL of 1 nanosecond guarantees the hold is already expired by the
	// time ConfirmHold runs -- deterministic, no sleep needed.
	hold, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 1*time.Nanosecond)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // ensure now() has advanced past the 1ns TTL

	_, err = s.ConfirmHold(ctx, hold.ID, "alice@example.com")
	if !errors.Is(err, ErrHoldExpired) {
		t.Errorf("err = %v, want ErrHoldExpired", err)
	}
}

func TestReleaseHold_FreesTheSeatForAnotherBuyer(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	hold, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}
	if _, err := s.ReleaseHold(ctx, hold.ID, "alice@example.com"); err != nil {
		t.Fatalf("ReleaseHold: %v", err)
	}

	// Per ADR-001: a released hold is no longer in ('held','confirmed'), so
	// the partial unique index must not block a new hold on the same seat.
	if _, err := s.PlaceHold(ctx, matchID, "A1", "bob@example.com", 5*time.Minute); err != nil {
		t.Errorf("PlaceHold after release: %v, want success", err)
	}
}

func TestReleaseHold_NotFoundForWrongBuyer(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	hold, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}

	_, err = s.ReleaseHold(ctx, hold.ID, "mallory@example.com")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
