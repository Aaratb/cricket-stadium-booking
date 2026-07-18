package store

import (
	"context"
	"fmt"
	"time"
)

// BookingSummaryRow is the read model used by the buyer-facing booking
// history endpoint. RefundStatus is nil for an active confirmed booking and
// populated once cancellation has created a refund.
type BookingSummaryRow struct {
	ID           int64
	MatchID      string
	SeatID       string
	Status       string
	ConfirmedAt  *time.Time
	CancelledAt  *time.Time
	RefundStatus *string
}

// ListBookings returns only completed bookings owned by buyerID for the
// requested match. A released/replaced hold also has status='cancelled', so
// confirmed_at IS NOT NULL is essential: without it the user-facing history
// would incorrectly present abandoned holds as cancelled purchases.
func (s *Store) ListBookings(ctx context.Context, matchID, buyerID string) ([]BookingSummaryRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.id, b.match_id, b.seat_id, b.status,
		       b.confirmed_at, b.cancelled_at, refund.status
		FROM bookings b
		LEFT JOIN LATERAL (
			SELECT r.status
			FROM refunds r
			WHERE r.booking_id = b.id
			ORDER BY r.requested_at DESC, r.id DESC
			LIMIT 1
		) refund ON true
		WHERE b.match_id = $1
		  AND b.buyer_id = $2
		  AND (
			b.status = 'confirmed'
			OR (b.status = 'cancelled' AND b.confirmed_at IS NOT NULL)
		  )
		ORDER BY COALESCE(b.cancelled_at, b.confirmed_at) DESC, b.id DESC`,
		matchID, buyerID)
	if err != nil {
		return nil, fmt.Errorf("list bookings: %w", err)
	}
	defer rows.Close()

	out := make([]BookingSummaryRow, 0)
	for rows.Next() {
		var row BookingSummaryRow
		if err := rows.Scan(
			&row.ID,
			&row.MatchID,
			&row.SeatID,
			&row.Status,
			&row.ConfirmedAt,
			&row.CancelledAt,
			&row.RefundStatus,
		); err != nil {
			return nil, fmt.Errorf("list bookings: scan: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bookings: %w", err)
	}
	return out, nil
}
