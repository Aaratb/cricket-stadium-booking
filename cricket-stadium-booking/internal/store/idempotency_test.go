package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPlaceHoldWithKey_ReplaysLiveHold(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()
	key := matchID + "-k1"

	first, err := s.PlaceHoldWithKey(ctx, matchID, "A1", "alice@example.com", 5*time.Minute, key)
	if err != nil {
		t.Fatalf("PlaceHoldWithKey: %v", err)
	}

	// The retry-after-lost-response case: same key, same parameters must
	// replay the original hold, not create a duplicate or report a false
	// conflict against its own booking.
	replay, err := s.PlaceHoldWithKey(ctx, matchID, "A1", "alice@example.com", 5*time.Minute, key)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.ID != first.ID {
		t.Errorf("replay ID = %d, want original %d", replay.ID, first.ID)
	}

	var active int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM bookings
		WHERE match_id = $1 AND seat_id = 'A1' AND status IN ('held','confirmed')`,
		matchID).Scan(&active); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if active != 1 {
		t.Errorf("active bookings = %d, want 1", active)
	}
}

func TestPlaceHoldWithKey_RejectsReuseWithDifferentParameters(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()
	key := matchID + "-k1"

	if _, err := s.PlaceHoldWithKey(ctx, matchID, "A1", "alice@example.com", 5*time.Minute, key); err != nil {
		t.Fatalf("PlaceHoldWithKey: %v", err)
	}

	// Same key, different seat: must never silently replay the A1 booking.
	_, err := s.PlaceHoldWithKey(ctx, matchID, "A2", "alice@example.com", 5*time.Minute, key)
	if !errors.Is(err, ErrIdempotencyKeyReuse) {
		t.Errorf("different seat: err = %v, want ErrIdempotencyKeyReuse", err)
	}

	// Same key, different buyer: replaying would leak alice's booking to bob.
	_, err = s.PlaceHoldWithKey(ctx, matchID, "A1", "bob@example.com", 5*time.Minute, key)
	if !errors.Is(err, ErrIdempotencyKeyReuse) {
		t.Errorf("different buyer: err = %v, want ErrIdempotencyKeyReuse", err)
	}
}

func TestPlaceHoldWithKey_ExpiredHoldRetriesFresh(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()
	key := matchID + "-k1"

	// 1ns TTL: already expired by the time the retry runs (same idiom as
	// TestConfirmHold_ExpiredRejected).
	first, err := s.PlaceHoldWithKey(ctx, matchID, "A1", "alice@example.com", 1*time.Nanosecond, key)
	if err != nil {
		t.Fatalf("PlaceHoldWithKey: %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	// The recorded booking is dead, so the retry must produce a fresh live
	// hold — not replay an expired row as success (a dead hold would fail
	// the client's subsequent confirm confusingly).
	second, err := s.PlaceHoldWithKey(ctx, matchID, "A1", "alice@example.com", 5*time.Minute, key)
	if err != nil {
		t.Fatalf("retry after expiry: %v", err)
	}
	if second.ID == first.ID {
		t.Errorf("retry replayed dead hold %d, want a fresh hold", first.ID)
	}
	if second.Status != "held" || second.HoldExpiresAt == nil || !second.HoldExpiresAt.After(time.Now()) {
		t.Errorf("retry hold = %+v, want live 'held' with future expiry", second)
	}

	// The key must now map to the fresh booking: a further retry replays it.
	third, err := s.PlaceHoldWithKey(ctx, matchID, "A1", "alice@example.com", 5*time.Minute, key)
	if err != nil {
		t.Fatalf("replay of fresh hold: %v", err)
	}
	if third.ID != second.ID {
		t.Errorf("replay ID = %d, want re-pointed booking %d", third.ID, second.ID)
	}
}

func TestPlaceHoldWithKey_DeadKeyAndSeatTakenByOther(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()
	key := matchID + "-k1"

	if _, err := s.PlaceHoldWithKey(ctx, matchID, "A1", "alice@example.com", 1*time.Nanosecond, key); err != nil {
		t.Fatalf("PlaceHoldWithKey: %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	// Bob takes the seat after alice's hold expired.
	if _, err := s.PlaceHold(ctx, matchID, "A1", "bob@example.com", 5*time.Minute); err != nil {
		t.Fatalf("PlaceHold (bob): %v", err)
	}

	// Alice's retry finds a dead key mapping and a seat genuinely held by
	// someone else: the truthful answer is a seat conflict.
	_, err := s.PlaceHoldWithKey(ctx, matchID, "A1", "alice@example.com", 5*time.Minute, key)
	if !errors.Is(err, ErrSeatUnavailable) {
		t.Errorf("err = %v, want ErrSeatUnavailable", err)
	}
}

// TestPlaceHoldWithKey_ConcurrentSameKey exercises the unique-violation
// replay paths: two racing requests with the same key must converge on one
// booking — the loser replays the winner's committed hold instead of
// reporting a false conflict or double-booking.
func TestPlaceHoldWithKey_ConcurrentSameKey(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()
	key := matchID + "-k1"

	const racers = 8
	ids := make([]int64, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			row, err := s.PlaceHoldWithKey(ctx, matchID, "A1", "alice@example.com", 5*time.Minute, key)
			ids[i], errs[i] = row.ID, err
		}(i)
	}
	wg.Wait()

	for i := 0; i < racers; i++ {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Errorf("racer %d got booking %d, racer 0 got %d — all must converge", i, ids[i], ids[0])
		}
	}

	var active int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM bookings
		WHERE match_id = $1 AND seat_id = 'A1' AND status IN ('held','confirmed')`,
		matchID).Scan(&active); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if active != 1 {
		t.Errorf("active bookings = %d, want 1", active)
	}
}
