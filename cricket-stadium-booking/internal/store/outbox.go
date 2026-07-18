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

// claimLeaseTTL bounds how long a claimed-but-unprocessed event stays
// invisible to other workers. A worker that crashes mid-processing leaves its
// claim behind; once the lease expires, another worker reclaims the event.
// It must comfortably exceed a normal process-one-event duration.
const claimLeaseTTL = 30 * time.Second

// PollUnprocessed atomically claims a bounded batch of unprocessed events and
// returns them. It is safe to run from multiple workers concurrently: the
// FOR UPDATE SKIP LOCKED in the sub-select means two workers never claim the
// same rows, so the outbox drain scales horizontally instead of being a
// single-worker bottleneck (real-scale-topology.md). The claimed_at stamp is
// a lease, not a completion marker — MarkProcessed still records durable
// completion after the side effect succeeds, preserving at-least-once.
//
// The batch stays bounded (customer-pain-points.md item 3: never drain
// everything in one pass — that risks a long-running transaction that starves
// autovacuum, per stress-test.md scenario 4b).
func (s *Store) PollUnprocessed(ctx context.Context, limit int) ([]OutboxEvent, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE outbox_events SET claimed_at = now()
		WHERE id IN (
			SELECT id FROM outbox_events
			WHERE processed_at IS NULL
			  AND (claimed_at IS NULL OR claimed_at < now() - make_interval(secs => $2))
			ORDER BY created_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, event_type, booking_id, payload`,
		limit, claimLeaseTTL.Seconds())
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

// ReleaseClaim immediately returns a claimed-but-unprocessed event to the
// pollable pool instead of leaving it invisible until the claim lease
// expires. The worker calls it when a side effect fails transiently, so the
// retry happens on the next poll (~seconds) rather than a full claimLeaseTTL
// later. Best-effort: if this fails, lease expiry remains the backstop.
func (s *Store) ReleaseClaim(ctx context.Context, eventID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE outbox_events SET claimed_at = NULL
		WHERE id = $1 AND processed_at IS NULL`, eventID)
	if err != nil {
		return fmt.Errorf("release outbox claim: %w", err)
	}
	return nil
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
