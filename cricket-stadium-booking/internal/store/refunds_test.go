package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCancelBooking_Success(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	hold, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}
	if _, err := s.ConfirmHold(ctx, hold.ID, "alice@example.com"); err != nil {
		t.Fatalf("ConfirmHold: %v", err)
	}

	cancelled, err := s.CancelBooking(ctx, hold.ID, "alice@example.com")
	if err != nil {
		t.Fatalf("CancelBooking: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", cancelled.Status)
	}

	// ADR-002: cancelling must atomically write a pending refund AND free
	// the seat for a new hold, in one transaction.
	var refundStatus string
	if err := s.pool.QueryRow(ctx, `SELECT status FROM refunds WHERE booking_id = $1`, hold.ID).Scan(&refundStatus); err != nil {
		t.Fatalf("query refund: %v", err)
	}
	if refundStatus != "pending" {
		t.Errorf("refund status = %q, want pending", refundStatus)
	}

	var outboxCount int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE booking_id = $1 AND event_type = 'refund_requested'`, hold.ID).Scan(&outboxCount); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if outboxCount != 1 {
		t.Errorf("outbox event count = %d, want 1", outboxCount)
	}

	if _, err := s.PlaceHold(ctx, matchID, "A1", "bob@example.com", 5*time.Minute); err != nil {
		t.Errorf("PlaceHold after cancel: %v, want success (seat should be free)", err)
	}
}

func TestCancelBooking_RejectsNonConfirmedBooking(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	hold, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}
	// Never confirmed -- still 'held', not 'confirmed'.

	_, err = s.CancelBooking(ctx, hold.ID, "alice@example.com")
	if !errors.Is(err, ErrHoldExpired) {
		t.Errorf("err = %v, want ErrHoldExpired (cancel only applies to confirmed bookings)", err)
	}
}

// TestCancelBooking_RejectsAfterMatchStart proves the customer-pain-points.md
// fix: cancelling a booking for a match that already happened must never
// silently succeed, independent of whatever cutoff-window policy exists.
func TestCancelBooking_RejectsAfterMatchStart(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	hold, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}
	if _, err := s.ConfirmHold(ctx, hold.ID, "alice@example.com"); err != nil {
		t.Fatalf("ConfirmHold: %v", err)
	}

	if _, err := s.pool.Exec(ctx, `UPDATE matches SET start_time = now() - interval '1 hour' WHERE id = $1`, matchID); err != nil {
		t.Fatalf("backdate match start_time: %v", err)
	}

	_, err = s.CancelBooking(ctx, hold.ID, "alice@example.com")
	if !errors.Is(err, ErrHoldExpired) {
		t.Errorf("err = %v, want ErrHoldExpired (match already started)", err)
	}
}

func TestMarkRefundStatus_Idempotent(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	hold, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}
	if _, err := s.ConfirmHold(ctx, hold.ID, "alice@example.com"); err != nil {
		t.Fatalf("ConfirmHold: %v", err)
	}
	if _, err := s.CancelBooking(ctx, hold.ID, "alice@example.com"); err != nil {
		t.Fatalf("CancelBooking: %v", err)
	}

	var refundID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM refunds WHERE booking_id = $1`, hold.ID).Scan(&refundID); err != nil {
		t.Fatalf("query refund id: %v", err)
	}

	// First mark succeeds; a second (simulating a worker retry after crash)
	// must not error and must not double-apply -- this is what makes the
	// worker's crash-and-retry loop safe.
	if err := s.MarkRefundStatus(ctx, refundID, "refunded", "ref-1"); err != nil {
		t.Fatalf("first MarkRefundStatus: %v", err)
	}
	if err := s.MarkRefundStatus(ctx, refundID, "refunded", "ref-2"); err != nil {
		t.Fatalf("second MarkRefundStatus: %v", err)
	}

	var status, externalRef string
	if err := s.pool.QueryRow(ctx, `SELECT status, external_ref FROM refunds WHERE id = $1`, refundID).Scan(&status, &externalRef); err != nil {
		t.Fatalf("query refund: %v", err)
	}
	if status != "refunded" || externalRef != "ref-1" {
		t.Errorf("status=%q externalRef=%q, want refunded/ref-1 (second call should be a no-op)", status, externalRef)
	}
}
