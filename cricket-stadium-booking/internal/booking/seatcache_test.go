package booking

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestSeatCache_CollapsesLoadsAndInvalidates(t *testing.T) {
	c := newSeatCache(time.Minute)
	ctx := context.Background()
	var calls atomic.Int32
	loader := func() ([]Seat, error) {
		calls.Add(1)
		return []Seat{{SeatID: "A1", Status: "available"}}, nil
	}

	for i := 0; i < 3; i++ {
		if _, err := c.load(ctx, "m1", loader); err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("loader calls = %d, want 1 (cache hit after first load)", n)
	}

	c.invalidate("m1")
	if _, err := c.load(ctx, "m1", loader); err != nil {
		t.Fatalf("load after invalidate: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("loader calls = %d, want 2 (invalidate must force a re-read)", n)
	}
}

// TestSeatCache_WaiterHonorsOwnDeadline pins the DoChan contract: a caller
// whose context is done stops waiting on the shared flight immediately, and
// the flight itself keeps running to completion — its result still lands in
// the cache for the next reader.
func TestSeatCache_WaiterHonorsOwnDeadline(t *testing.T) {
	c := newSeatCache(time.Minute)
	release := make(chan struct{})
	loaderDone := make(chan struct{})
	loader := func() ([]Seat, error) {
		<-release
		close(loaderDone)
		return []Seat{{SeatID: "A1", Status: "held"}}, nil
	}

	// Leader starts the flight and blocks in the loader.
	leaderResult := make(chan error, 1)
	go func() {
		_, err := c.load(context.Background(), "m1", loader)
		leaderResult <- err
	}()

	// Give the leader time to enter the flight, then join it with an
	// already-cancelled context: the waiter must return promptly with the
	// context error instead of blocking as long as the flight runs.
	time.Sleep(20 * time.Millisecond)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := c.load(cancelled, "m1", loader)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter err = %v, want context.Canceled", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("waiter blocked %v despite cancelled context", waited)
	}

	// The abandoned waiter must not have cancelled the shared flight.
	close(release)
	select {
	case <-loaderDone:
	case <-time.After(2 * time.Second):
		t.Fatal("flight never completed after waiter abandoned it")
	}
	if err := <-leaderResult; err != nil {
		t.Fatalf("leader err = %v, want success", err)
	}
	if entry, ok := c.get("m1"); !ok || len(entry.seats) != 1 {
		t.Errorf("cache not populated by completed flight (ok=%v)", ok)
	}
}

func TestSeatSnapshotVersion_StableAndSensitiveToRepresentation(t *testing.T) {
	expiresAt := time.Date(2026, time.July, 18, 12, 30, 0, 123, time.UTC)
	seats := []Seat{{
		SeatID:        "A1",
		Section:       "NORTH",
		Status:        "held",
		HoldExpiresAt: &expiresAt,
	}}

	version := seatSnapshotVersion("m1", seats)
	if version == "" {
		t.Fatal("version is empty")
	}
	if again := seatSnapshotVersion("m1", seats); again != version {
		t.Fatalf("same snapshot version = %q, want %q", again, version)
	}

	changed := append([]Seat(nil), seats...)
	changed[0].Status = "confirmed"
	if got := seatSnapshotVersion("m1", changed); got == version {
		t.Fatal("status change did not change snapshot version")
	}
	if got := seatSnapshotVersion("m2", seats); got == version {
		t.Fatal("match change did not change snapshot version")
	}
}
