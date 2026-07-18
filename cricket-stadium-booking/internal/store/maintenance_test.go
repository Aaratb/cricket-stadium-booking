package store

import (
	"context"
	"testing"
	"time"
)

func TestSweepExpiredHolds_ExpiresOnlyDeadHolds(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	dead, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 1*time.Nanosecond)
	if err != nil {
		t.Fatalf("PlaceHold (dead): %v", err)
	}
	live, err := s.PlaceHold(ctx, matchID, "A2", "bob@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold (live): %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	if _, err := s.SweepExpiredHolds(ctx, 1000); err != nil {
		t.Fatalf("SweepExpiredHolds: %v", err)
	}

	var status string
	if err := s.pool.QueryRow(ctx, `SELECT status FROM bookings WHERE id = $1`, dead.ID).Scan(&status); err != nil {
		t.Fatalf("query dead hold: %v", err)
	}
	if status != "expired" {
		t.Errorf("dead hold status = %q, want expired", status)
	}
	if err := s.pool.QueryRow(ctx, `SELECT status FROM bookings WHERE id = $1`, live.ID).Scan(&status); err != nil {
		t.Fatalf("query live hold: %v", err)
	}
	if status != "held" {
		t.Errorf("live hold status = %q, want held (sweep must not touch unexpired holds)", status)
	}
}

func TestPruneIdempotencyKeys_RemovesOnlyKeysPastHorizon(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()
	oldKey, freshKey := matchID+"-old", matchID+"-fresh"

	if _, err := s.PlaceHoldWithKey(ctx, matchID, "A1", "alice@example.com", 5*time.Minute, oldKey); err != nil {
		t.Fatalf("PlaceHoldWithKey (old): %v", err)
	}
	if _, err := s.PlaceHoldWithKey(ctx, matchID, "A2", "bob@example.com", 5*time.Minute, freshKey); err != nil {
		t.Fatalf("PlaceHoldWithKey (fresh): %v", err)
	}
	// Age the first key past the retry horizon.
	if _, err := s.pool.Exec(ctx,
		`UPDATE idempotency_keys SET created_at = now() - interval '25 hours' WHERE key = $1`, oldKey); err != nil {
		t.Fatalf("age key: %v", err)
	}

	if _, err := s.PruneIdempotencyKeys(ctx, 24*time.Hour, 1000); err != nil {
		t.Fatalf("PruneIdempotencyKeys: %v", err)
	}

	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys WHERE key = $1`, oldKey).Scan(&count); err != nil {
		t.Fatalf("query old key: %v", err)
	}
	if count != 0 {
		t.Errorf("old key still present, want pruned")
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys WHERE key = $1`, freshKey).Scan(&count); err != nil {
		t.Fatalf("query fresh key: %v", err)
	}
	if count != 1 {
		t.Errorf("fresh key count = %d, want 1 (prune must not touch keys inside the horizon)", count)
	}
}

// TestReleaseClaim_MakesEventImmediatelyRepollable covers the transient-
// failure retry path: without ReleaseClaim, a claimed event stays invisible
// for a full claimLeaseTTL even though the worker wants to retry it on the
// very next poll.
func TestReleaseClaim_MakesEventImmediatelyRepollable(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	// Generate a real outbox event via the cancel flow.
	hold, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}
	if _, err := s.ConfirmHold(ctx, hold.ID, "alice@example.com"); err != nil {
		t.Fatalf("ConfirmHold: %v", err)
	}
	if _, err := s.CancelBooking(ctx, hold.ID, "alice@example.com"); err != nil {
		t.Fatalf("CancelBooking: %v", err)
	}
	var eventID int64
	if err := s.pool.QueryRow(ctx,
		`SELECT id FROM outbox_events WHERE booking_id = $1`, hold.ID).Scan(&eventID); err != nil {
		t.Fatalf("find outbox event: %v", err)
	}

	claimedAt := func() *time.Time {
		var ts *time.Time
		if err := s.pool.QueryRow(ctx,
			`SELECT claimed_at FROM outbox_events WHERE id = $1`, eventID).Scan(&ts); err != nil {
			t.Fatalf("query claimed_at: %v", err)
		}
		return ts
	}

	// Claim it (PollUnprocessed stamps the lease on everything it returns).
	if _, err := s.PollUnprocessed(ctx, 1000); err != nil {
		t.Fatalf("PollUnprocessed: %v", err)
	}
	if claimedAt() == nil {
		t.Fatal("event not claimed after poll, want claimed_at set")
	}

	// Transient side-effect failure -> worker releases the claim: the event
	// must be pollable again immediately, not after claimLeaseTTL.
	if err := s.ReleaseClaim(ctx, eventID); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}
	if claimedAt() != nil {
		t.Fatal("claimed_at still set after ReleaseClaim, want NULL")
	}

	events, err := s.PollUnprocessed(ctx, 1000)
	if err != nil {
		t.Fatalf("PollUnprocessed after release: %v", err)
	}
	found := false
	for _, e := range events {
		if e.ID == eventID {
			found = true
		}
	}
	if !found {
		t.Error("released event not returned by the next poll")
	}

	// Once processed, ReleaseClaim must not resurrect the event.
	if err := s.MarkProcessed(ctx, eventID); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	if err := s.ReleaseClaim(ctx, eventID); err != nil {
		t.Fatalf("ReleaseClaim after processed: %v", err)
	}
	var processed *time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT processed_at FROM outbox_events WHERE id = $1`, eventID).Scan(&processed); err != nil {
		t.Fatalf("query processed_at: %v", err)
	}
	if processed == nil {
		t.Error("processed_at cleared, want intact (ReleaseClaim only applies to unprocessed events)")
	}
}
