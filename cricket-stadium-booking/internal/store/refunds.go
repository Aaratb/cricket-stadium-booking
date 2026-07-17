package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CancelBooking implements ADR-002's cancel flow as a single atomic
// transaction: CAS the booking to cancelled (with the start_time guard —
// a correctness fix, not a policy choice: cancelling a booking for a match
// that already happened must never silently succeed), insert a refunds row,
// and insert an outbox event, all in one commit. The API response returns
// immediately after this commits — the actual refund happens later, async,
// via the outbox worker.
func (s *Store) CancelBooking(ctx context.Context, bookingID int64, buyerID string) (BookingRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BookingRow{}, fmt.Errorf("cancel booking: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var row BookingRow
	row.ID, row.BuyerID, row.Status = bookingID, buyerID, "cancelled"
	err = tx.QueryRow(ctx, `
		UPDATE bookings b SET status = 'cancelled', cancelled_at = now()
		FROM matches m
		WHERE b.id = $1 AND b.buyer_id = $2 AND b.status = 'confirmed'
		  AND b.match_id = m.id AND now() < m.start_time
		RETURNING b.match_id, b.seat_id`,
		bookingID, buyerID).Scan(&row.MatchID, &row.SeatID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BookingRow{}, ErrHoldExpired // reused as "not cancellable" — booking not confirmed/owned/before start_time
		}
		return BookingRow{}, fmt.Errorf("cancel booking: cas update: %w", err)
	}

	var refundID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO refunds (booking_id, status) VALUES ($1, 'pending') RETURNING id`,
		bookingID).Scan(&refundID); err != nil {
		return BookingRow{}, fmt.Errorf("cancel booking: insert refund: %w", err)
	}

	if err := insertOutboxEvent(ctx, tx, "refund_requested", bookingID, map[string]any{"refund_id": refundID}); err != nil {
		return BookingRow{}, fmt.Errorf("cancel booking: insert outbox event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return BookingRow{}, fmt.Errorf("cancel booking: commit: %w", err)
	}
	return row, nil
}

// MarkRefundStatus is called by the outbox worker after driving the
// (stubbed) refund. Idempotent via the WHERE clause — a retried event that
// already succeeded simply affects zero rows.
func (s *Store) MarkRefundStatus(ctx context.Context, refundID int64, status, externalRef string) error {
	now := time.Now()
	_, err := s.pool.Exec(ctx, `
		UPDATE refunds SET status = $1, completed_at = $2, external_ref = $3
		WHERE id = $4 AND status = 'pending'`, status, now, externalRef, refundID)
	if err != nil {
		return fmt.Errorf("mark refund status: %w", err)
	}
	return nil
}
