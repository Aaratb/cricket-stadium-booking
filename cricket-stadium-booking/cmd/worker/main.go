// cmd/worker drains the transactional outbox (ADR-002): it drives the
// (stubbed, per the standing safety rule against executing real financial
// transactions) refund side effect for each cancelled booking, completely
// decoupled from the request path.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"stadiumbooking/internal/config"
	"stadiumbooking/internal/store"
)

const (
	pollInterval = 2 * time.Second
	// sweepBatchSize bounds each expired-hold sweep so it stays a short
	// transaction. Larger than a typical poll batch because expiring a row is
	// cheaper than driving a refund side effect.
	sweepBatchSize = 1000
	// idempotencyKeyRetention is the retry horizon documented in migration
	// 0006: a key older than this is no longer replayable and is pruned so
	// the table (and the FK pins it holds on bookings rows) stays bounded.
	idempotencyKeyRetention = 24 * time.Hour
	pruneBatchSize          = 1000
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	pool, err := store.NewPool(ctx, cfg)
	if err != nil {
		log.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	st := store.New(pool)

	log.Printf("outbox worker started, poll interval=%s, batch size=%d", pollInterval, cfg.OutboxBatchSize)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("worker shutting down")
			return
		case <-ticker.C:
			processBatch(ctx, st, cfg.OutboxBatchSize)
			sweepExpiredHolds(ctx, st)
			pruneIdempotencyKeys(ctx, st)
		}
	}
}

// sweepExpiredHolds reclaims abandoned expired holds so they don't bloat the
// bookings table/index. Best-effort: a failure is logged and retried on the
// next tick, and correctness never depends on it (reads derive status live).
func sweepExpiredHolds(ctx context.Context, st *store.Store) {
	n, err := st.SweepExpiredHolds(ctx, sweepBatchSize)
	if err != nil {
		log.Printf("sweep expired holds: %v", err)
		return
	}
	if n > 0 {
		log.Printf("swept %d expired holds", n)
	}
}

// pruneIdempotencyKeys drops keys past the retry horizon. Best-effort for
// the same reasons as sweepExpiredHolds: a failure is logged and retried on
// the next tick, and correctness never depends on it.
func pruneIdempotencyKeys(ctx context.Context, st *store.Store) {
	n, err := st.PruneIdempotencyKeys(ctx, idempotencyKeyRetention, pruneBatchSize)
	if err != nil {
		log.Printf("prune idempotency keys: %v", err)
		return
	}
	if n > 0 {
		log.Printf("pruned %d idempotency keys", n)
	}
}

func processBatch(ctx context.Context, st *store.Store, batchSize int) {
	events, err := st.PollUnprocessed(ctx, batchSize)
	if err != nil {
		log.Printf("poll unprocessed: %v", err)
		return
	}
	for _, e := range events {
		// An unrecognized event type or unparseable payload is a permanent
		// failure — retrying won't fix it, and leaving it unprocessed would
		// head-of-line-block every future event behind it forever (the
		// batch is always the oldest N unprocessed rows). Mark it processed
		// (dead-lettered via the log) rather than looping on it indefinitely.
		// Only a MarkRefundStatus DB error below is left unprocessed to retry,
		// since that failure mode is plausibly transient.
		if e.EventType != "refund_requested" {
			log.Printf("DEAD-LETTER: unknown event type %q on outbox event %d", e.EventType, e.ID)
			if err := st.MarkProcessed(ctx, e.ID); err != nil {
				log.Printf("mark outbox event %d processed: %v", e.ID, err)
			}
			continue
		}
		var payload struct {
			RefundID int64 `json:"refund_id"`
		}
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			log.Printf("DEAD-LETTER: bad payload on outbox event %d: %v", e.ID, err)
			if err := st.MarkProcessed(ctx, e.ID); err != nil {
				log.Printf("mark outbox event %d processed: %v", e.ID, err)
			}
			continue
		}

		externalRef := stubbedGatewayRefund(payload.RefundID)

		if err := st.MarkRefundStatus(ctx, payload.RefundID, "refunded", externalRef); err != nil {
			log.Printf("mark refund %d status: %v", payload.RefundID, err)
			// Return the claim so the retry really does happen on the next
			// poll — without this, the claimed_at lease stamped by
			// PollUnprocessed hides the event from every worker for a full
			// claimLeaseTTL. Lease expiry remains the backstop if the
			// release itself fails.
			if rerr := st.ReleaseClaim(ctx, e.ID); rerr != nil {
				log.Printf("release claim on outbox event %d: %v", e.ID, rerr)
			}
			continue // retried next poll, idempotently
		}
		if err := st.MarkProcessed(ctx, e.ID); err != nil {
			log.Printf("mark outbox event %d processed: %v", e.ID, err)
		}
	}
}

// stubbedGatewayRefund never executes a real financial transaction — it
// simulates a successful gateway response, matching how payment capture is
// already stubbed elsewhere in this build. refundID + nanosecond precision
// keeps the ref unique even for two refunds processed in the same second.
func stubbedGatewayRefund(refundID int64) string {
	return fmt.Sprintf("stub-refund-ref-%d-%d", refundID, time.Now().UnixNano())
}
