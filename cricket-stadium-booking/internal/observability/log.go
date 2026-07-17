// Package observability provides structured logging on hold/confirm/expire
// transitions, feeding the PRD's own success metrics (p95 latency,
// confirmed/sec, retry counts) which otherwise have no home outside the
// load-test harness's own report (stress-test.md gap #5).
package observability

import (
	"context"
	"log/slog"
	"os"
	"time"
)

var logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

// LogTransition logs match/seat/timing/error context only. Never pass
// buyer_id or any other PII into this function or its attrs — these logs
// are structured JSON on stdout with no redaction layer in front of them.
func LogTransition(ctx context.Context, op string, matchID, seatID string, start time.Time, err error) {
	attrs := []any{
		"op", op,
		"match_id", matchID,
		"seat_id", seatID,
		"duration_ms", time.Since(start).Milliseconds(),
	}
	if err != nil {
		logger.WarnContext(ctx, "booking_transition_failed", append(attrs, "error", err.Error())...)
		return
	}
	logger.InfoContext(ctx, "booking_transition_ok", attrs...)
}
