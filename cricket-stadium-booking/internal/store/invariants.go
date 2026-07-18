package store

import (
	"context"
	"fmt"
	"time"
)

// SweepExpiredHolds proactively marks a bounded batch of holds whose TTL has
// passed as 'expired'. Lazy expiry (ADR-001) already keeps reads correct
// without this — a held-but-expired row reads as available, and PlaceHold
// reclaims it on the next attempt for that exact seat. The sweeper's only job
// is to stop abandoned expired holds (seats nobody re-holds) from lingering
// as 'held' rows forever and bloating the hot bookings table and its index.
// The LIMIT keeps each sweep short so it never becomes a long-running
// transaction that starves autovacuum (stress-test.md scenario 4b), and
// FOR UPDATE SKIP LOCKED partitions the work across concurrent workers the
// same way PollUnprocessed does — without it, N workers on the same tick
// all select the identical oldest rows and serialize on each other's row
// locks doing redundant re-expires. Uses ix_bookings_held_expiry (migration
// 0007) so an idle sweep is an empty index range scan, not a table scan.
func (s *Store) SweepExpiredHolds(ctx context.Context, limit int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE bookings SET status = 'expired'
		WHERE id IN (
			SELECT id FROM bookings
			WHERE status = 'held' AND hold_expires_at < now()
			ORDER BY hold_expires_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)`, limit)
	if err != nil {
		return 0, fmt.Errorf("sweep expired holds: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PruneIdempotencyKeys deletes idempotency keys older than the retry
// horizon, implementing the retention that migration 0006 documents: keys
// otherwise accumulate one row per keyed hold forever, and their FK pins
// the referenced bookings rows against any future archival. Bounded batch +
// SKIP LOCKED for the same reasons as SweepExpiredHolds; the created_at
// scan is served by ix_idempotency_keys_created. The horizon is bound via
// the microsecond-multiply idiom (see placeHold) rather than a formatted
// interval string.
func (s *Store) PruneIdempotencyKeys(ctx context.Context, olderThan time.Duration, limit int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM idempotency_keys
		WHERE key IN (
			SELECT key FROM idempotency_keys
			WHERE created_at < now() - ($1 * interval '1 microsecond')
			ORDER BY created_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)`, olderThan.Microseconds(), limit)
	if err != nil {
		return 0, fmt.Errorf("prune idempotency keys: %w", err)
	}
	return tag.RowsAffected(), nil
}

// OversoldSeats is the actual correctness proof: any row here means the
// partial unique index failed to prevent a double-sell. Used by
// cmd/loadtest against the DB directly, per spec.md's requirement to
// verify from live Postgres state, not client-side counters.
func (s *Store) OversoldSeats(ctx context.Context) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT match_id, seat_id, count(*) AS c
			FROM bookings
			WHERE status = 'confirmed'
			GROUP BY match_id, seat_id
			HAVING count(*) > 1
		) dup`).Scan(&count)
	return count, err
}

// StuckHolds counts holds whose TTL has passed but are still marked 'held'
// (not yet lazily reclaimed by a subsequent hold attempt on that seat).
func (s *Store) StuckHolds(ctx context.Context) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM bookings
		WHERE status = 'held' AND hold_expires_at < now()`).Scan(&count)
	return count, err
}

func (s *Store) CountByStatus(ctx context.Context, status string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM bookings WHERE status = $1`, status).Scan(&count)
	return count, err
}
