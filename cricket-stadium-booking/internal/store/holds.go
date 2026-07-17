package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// PlaceHold implements ADR-001's hold path: lazily expire any stale hold on
// this seat, then attempt to insert a new 'held' row. A concurrent winner on
// the same seat surfaces here as a unique_violation (23505) on
// ux_bookings_active_seat — fail-fast, not lock-and-wait.
func (s *Store) PlaceHold(ctx context.Context, matchID, seatID, buyerID string, ttl time.Duration) (BookingRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BookingRow{}, fmt.Errorf("place hold: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status = 'expired'
		WHERE match_id = $1 AND seat_id = $2
		  AND status = 'held' AND hold_expires_at < now()`,
		matchID, seatID); err != nil {
		return BookingRow{}, fmt.Errorf("place hold: expire stale hold: %w", err)
	}

	var row BookingRow
	row.MatchID, row.SeatID, row.BuyerID, row.Status = matchID, seatID, buyerID, "held"
	// Bind ttl as a microsecond count multiplied into an interval, not as a
	// formatted string: Go's time.Duration.String() emits units ("µs", "ns")
	// that Postgres's interval parser rejects outright (verified: SELECT
	// '500µs'::interval errors). Multiplying a numeric parameter avoids the
	// format mismatch entirely.
	err = tx.QueryRow(ctx, `
		INSERT INTO bookings (match_id, seat_id, buyer_id, status, hold_expires_at)
		VALUES ($1, $2, $3, 'held', now() + ($4 * interval '1 microsecond'))
		RETURNING id, hold_expires_at`,
		matchID, seatID, buyerID, ttl.Microseconds()).Scan(&row.ID, &row.HoldExpiresAt)
	if err != nil {
		if isUniqueViolation(err) {
			return BookingRow{}, ErrSeatUnavailable
		}
		return BookingRow{}, fmt.Errorf("place hold: insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return BookingRow{}, fmt.Errorf("place hold: commit: %w", err)
	}
	return row, nil
}

// ConfirmHold implements ADR-001's confirm path: a single compare-and-swap
// UPDATE guarded by status='held' AND hold_expires_at > now(). Zero rows
// affected means the hold was already expired, confirmed, or never owned by
// this buyer — all collapse to ErrHoldExpired, matching the API contract.
func (s *Store) ConfirmHold(ctx context.Context, holdID int64, buyerID string) (BookingRow, error) {
	var row BookingRow
	row.ID, row.BuyerID, row.Status = holdID, buyerID, "confirmed"
	err := s.pool.QueryRow(ctx, `
		UPDATE bookings
		SET status = 'confirmed', confirmed_at = now(), hold_expires_at = NULL
		WHERE id = $1 AND buyer_id = $2
		  AND status = 'held' AND hold_expires_at > now()
		RETURNING match_id, seat_id, confirmed_at`,
		holdID, buyerID).Scan(&row.MatchID, &row.SeatID, &row.ConfirmedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BookingRow{}, ErrHoldExpired
		}
		return BookingRow{}, fmt.Errorf("confirm hold: %w", err)
	}
	return row, nil
}

// ReleaseHold is the voluntary early-cancel path (buyer changes their mind
// before the hold expires). Same CAS discipline as confirm.
func (s *Store) ReleaseHold(ctx context.Context, holdID int64, buyerID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE bookings SET status = 'cancelled', cancelled_at = now()
		WHERE id = $1 AND buyer_id = $2 AND status = 'held'`,
		holdID, buyerID)
	if err != nil {
		return fmt.Errorf("release hold: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}
