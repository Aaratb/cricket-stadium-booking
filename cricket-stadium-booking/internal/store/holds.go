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
	return s.placeHold(ctx, matchID, seatID, buyerID, ttl, "")
}

// PlaceHoldWithKey is PlaceHold plus client-supplied idempotency. A retried
// request carrying the same idempotencyKey and the same match/seat/buyer
// returns the original hold — as long as that hold is still live — instead
// of creating a duplicate or reporting a false ErrSeatUnavailable when the
// retry races its own still-in-flight original. The same key with different
// request parameters is rejected with ErrIdempotencyKeyReuse (replaying a
// booking the caller didn't ask for would leak another request's data), and
// a key whose recorded booking has since expired or been cancelled lets the
// retry proceed as a fresh attempt. An empty key is identical to PlaceHold.
func (s *Store) PlaceHoldWithKey(ctx context.Context, matchID, seatID, buyerID string, ttl time.Duration, idempotencyKey string) (BookingRow, error) {
	return s.placeHold(ctx, matchID, seatID, buyerID, ttl, idempotencyKey)
}

func (s *Store) placeHold(ctx context.Context, matchID, seatID, buyerID string, ttl time.Duration, idemKey string) (BookingRow, error) {
	// Idempotent replay fast path, before any transaction opens: this key
	// already produced a booking. This is the common retry-after-lost-response
	// case, and it resolves without opening a transaction or touching the
	// seat — a guaranteed-miss lookup on a first-time request costs one pool
	// round trip instead of lengthening the contended seat transaction.
	//
	// staleBookingID records a mapping to a dead (expired/cancelled) booking:
	// the retry proceeds as a fresh attempt, and the key row is re-pointed at
	// the new booking inside the transaction below.
	var staleBookingID int64
	if idemKey != "" {
		row, state, err := s.lookupIdempotentHold(ctx, matchID, seatID, buyerID, idemKey)
		switch {
		case err != nil:
			return BookingRow{}, err
		case state == idemMismatch:
			return BookingRow{}, ErrIdempotencyKeyReuse
		case state == idemLive:
			return row, nil
		case state == idemDead:
			staleBookingID = row.ID
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BookingRow{}, fmt.Errorf("place hold: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Serialize replacement attempts for one buyer within one match. Without
	// this transaction-scoped lock, two tabs can update different previous
	// hold rows and then deadlock (or both insert before either observes the
	// other). The 64-bit hash keeps the lock key compact; the unique index in
	// migration 0008 remains the authoritative collision-proof backstop.
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
		  hashtextextended(json_build_array($1::text, $2::text)::text, 0)
		)`, matchID, buyerID); err != nil {
		return BookingRow{}, fmt.Errorf("place hold: lock buyer match: %w", err)
	}

	if staleBookingID != 0 {
		// The key points at a dead booking; free it for this fresh attempt.
		// The booking_id guard means a concurrent request that already
		// re-pointed the key is left alone — our key INSERT below then hits
		// the unique violation and replays that winner instead.
		if _, err := tx.Exec(ctx,
			`DELETE FROM idempotency_keys WHERE key = $1 AND booking_id = $2`,
			idemKey, staleBookingID); err != nil {
			return BookingRow{}, fmt.Errorf("place hold: clear stale idempotency key: %w", err)
		}
	}

	// A buyer may own only one live hold per match. Replacing it belongs in
	// the same transaction as acquiring the new seat: if the target seat is
	// unavailable, the INSERT below fails and this cancellation rolls back,
	// leaving the buyer's original hold intact. Excluding the target seat also
	// preserves keyless retry semantics (a duplicate request for the same seat
	// remains a conflict instead of silently extending its TTL).
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status = 'cancelled', cancelled_at = now()
		WHERE match_id = $1 AND buyer_id = $2
		  AND status = 'held' AND seat_id <> $3`,
		matchID, buyerID, seatID); err != nil {
		return BookingRow{}, fmt.Errorf("place hold: release previous buyer hold: %w", err)
	}

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
			// Seat is already actively held. With an idempotency key, that
			// holder might be this request's own committed original (a retry
			// that raced past our fast path) — replay it rather than report a
			// false conflict. The lookup runs on the pool (inside
			// lookupIdempotentHold) because tx is now in an aborted state
			// after the failed insert.
			if idemKey != "" {
				r, state, lerr := s.lookupIdempotentHold(ctx, matchID, seatID, buyerID, idemKey)
				switch {
				case lerr != nil:
					// Surface the lookup failure: mapping it to
					// ErrSeatUnavailable would turn a transient error into a
					// false, non-retryable conflict for a hold the client may
					// actually own — the exact bug idempotency exists to fix.
					return BookingRow{}, lerr
				case state == idemMismatch:
					return BookingRow{}, ErrIdempotencyKeyReuse
				case state == idemLive:
					return r, nil
				}
			}
			return BookingRow{}, ErrSeatUnavailable
		}
		return BookingRow{}, fmt.Errorf("place hold: insert: %w", err)
	}

	if idemKey != "" {
		if _, err := tx.Exec(ctx,
			`INSERT INTO idempotency_keys (key, booking_id) VALUES ($1, $2)`,
			idemKey, row.ID); err != nil {
			if isUniqueViolation(err) {
				// A concurrent request with the same key committed first. Our
				// booking is redundant; the deferred Rollback discards it, and
				// we replay the winner's result. (The violating INSERT blocked
				// until that winner committed, so the pool-side lookup sees it.)
				r, state, lerr := s.lookupIdempotentHold(ctx, matchID, seatID, buyerID, idemKey)
				switch {
				case lerr != nil:
					return BookingRow{}, lerr
				case state == idemMismatch:
					return BookingRow{}, ErrIdempotencyKeyReuse
				case state == idemLive:
					return r, nil
				}
				return BookingRow{}, ErrSeatUnavailable
			}
			return BookingRow{}, fmt.Errorf("place hold: record idempotency key: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return BookingRow{}, fmt.Errorf("place hold: commit: %w", err)
	}
	return row, nil
}

// idemState classifies what an idempotency-key lookup found.
type idemState int

const (
	idemMiss     idemState = iota // key never seen
	idemLive                      // key maps to a live booking with matching parameters — replay it
	idemDead                      // key maps to an expired/cancelled booking with matching parameters — retry fresh
	idemMismatch                  // key maps to a booking with different match/seat/buyer — client error
)

// lookupIdempotentHold returns the booking previously recorded under idemKey,
// classified against the current request. A replay is only valid when the
// recorded booking matches the request's match/seat/buyer (anything else is
// a key-reuse client error — never a silent replay of a booking the caller
// didn't ask for) AND is still live: confirmed, or held with an unexpired
// TTL. Liveness is computed in SQL so it uses the database clock, the same
// clock every other expiry decision in this store uses. One JOIN, one round
// trip.
func (s *Store) lookupIdempotentHold(ctx context.Context, matchID, seatID, buyerID, idemKey string) (BookingRow, idemState, error) {
	var row BookingRow
	var live bool
	err := s.pool.QueryRow(ctx, `
		SELECT b.id, b.match_id, b.seat_id, b.buyer_id, b.status, b.hold_expires_at, b.confirmed_at,
		       (b.status = 'confirmed' OR (b.status = 'held' AND b.hold_expires_at > now())) AS live
		FROM idempotency_keys k
		JOIN bookings b ON b.id = k.booking_id
		WHERE k.key = $1`, idemKey).Scan(
		&row.ID, &row.MatchID, &row.SeatID, &row.BuyerID, &row.Status, &row.HoldExpiresAt, &row.ConfirmedAt, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return BookingRow{}, idemMiss, nil
	}
	if err != nil {
		return BookingRow{}, idemMiss, fmt.Errorf("place hold: lookup idempotency key: %w", err)
	}
	if row.MatchID != matchID || row.SeatID != seatID || row.BuyerID != buyerID {
		return BookingRow{}, idemMismatch, nil
	}
	if !live {
		return row, idemDead, nil
	}
	return row, idemLive, nil
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
// before the hold expires). Same CAS discipline as confirm. Returns the
// released hold's match_id so the caller can invalidate that match's cached
// seat map.
func (s *Store) ReleaseHold(ctx context.Context, holdID int64, buyerID string) (string, error) {
	var matchID string
	err := s.pool.QueryRow(ctx, `
		UPDATE bookings SET status = 'cancelled', cancelled_at = now()
		WHERE id = $1 AND buyer_id = $2 AND status = 'held'
		RETURNING match_id`,
		holdID, buyerID).Scan(&matchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("release hold: %w", err)
	}
	return matchID, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}
