package store

import "context"

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
