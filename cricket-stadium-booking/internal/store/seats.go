package store

import (
	"context"
	"fmt"
)

// ListSeats derives status live from current rows — no background job
// required for correctness (ADR-001). A held-but-expired row correctly
// reads as available here even before any sweeper (if ever built) reclaims it.
func (s *Store) ListSeats(ctx context.Context, matchID string) ([]SeatStatus, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.seat_id, s.section,
		       COALESCE(b.status, 'available') AS status,
		       b.hold_expires_at
		FROM seats s
		LEFT JOIN bookings b
		  ON b.match_id = s.match_id AND b.seat_id = s.seat_id
		  AND b.status IN ('held', 'confirmed')
		  AND (b.status = 'confirmed' OR b.hold_expires_at > now())
		WHERE s.match_id = $1
		ORDER BY s.section, s.seat_id`, matchID)
	if err != nil {
		return nil, fmt.Errorf("list seats: %w", err)
	}
	defer rows.Close()

	var out []SeatStatus
	for rows.Next() {
		var seat SeatStatus
		if err := rows.Scan(&seat.SeatID, &seat.Section, &seat.Status, &seat.HoldExpiresAt); err != nil {
			return nil, fmt.Errorf("list seats: scan: %w", err)
		}
		out = append(out, seat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list seats: %w", err)
	}
	return out, nil
}
