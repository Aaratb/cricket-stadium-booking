package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testStore connects to the real local Postgres instance and seeds an
// isolated match+seat fixture per test (unique ID via t.Name()+timestamp).
// Deliberately not mocked: this project's whole point is proving
// correctness against real Postgres transaction/constraint behavior, and a
// mock would test nothing about the guarantee ADR-001 actually makes.
func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, "postgres:///cricket_stadium_booking?host=/tmp")
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)

	matchID := fmt.Sprintf("test-%s-%d", t.Name(), time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `INSERT INTO matches (id, name, start_time) VALUES ($1, 'test match', now() + interval '7 days')`, matchID); err != nil {
		t.Fatalf("seed match: %v", err)
	}
	for _, seatID := range []string{"A1", "A2", "A3"} {
		if _, err := pool.Exec(ctx, `INSERT INTO seats (match_id, seat_id, section) VALUES ($1, $2, 'TEST')`, matchID, seatID); err != nil {
			t.Fatalf("seed seat %s: %v", seatID, err)
		}
	}

	return New(pool), matchID
}
