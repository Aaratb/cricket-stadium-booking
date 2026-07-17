package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type OutboxEvent struct {
	ID        int64
	EventType string
	BookingID int64
	Payload   []byte
}

// insertOutboxEvent writes within the caller's transaction — this is
// ADR-002's whole point: the event and the state change it describes commit
// atomically, together, or not at all.
func insertOutboxEvent(ctx context.Context, tx pgx.Tx, eventType string, bookingID int64, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("insert outbox event: marshal payload: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_events (event_type, booking_id, payload)
		VALUES ($1, $2, $3)`, eventType, bookingID, b)
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}
	return nil
}

// PollUnprocessed returns a bounded batch (customer-pain-points.md item 3:
// never drain everything unprocessed in one pass — that risks becoming a
// long-running transaction that starves autovacuum, per stress-test.md
// scenario 4b).
func (s *Store) PollUnprocessed(ctx context.Context, limit int) ([]OutboxEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, event_type, booking_id, payload
		FROM outbox_events
		WHERE processed_at IS NULL
		ORDER BY created_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("poll unprocessed outbox events: %w", err)
	}
	defer rows.Close()

	var out []OutboxEvent
	for rows.Next() {
		var e OutboxEvent
		if err := rows.Scan(&e.ID, &e.EventType, &e.BookingID, &e.Payload); err != nil {
			return nil, fmt.Errorf("poll unprocessed outbox events: scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("poll unprocessed outbox events: %w", err)
	}
	return out, nil
}

// MarkProcessed is safe to call more than once for the same event id —
// the second call simply affects zero rows, which is what makes the
// worker's crash-and-retry loop idempotent.
func (s *Store) MarkProcessed(ctx context.Context, eventID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE outbox_events SET processed_at = $1
		WHERE id = $2 AND processed_at IS NULL`, time.Now(), eventID)
	if err != nil {
		return fmt.Errorf("mark outbox event processed: %w", err)
	}
	return nil
}
