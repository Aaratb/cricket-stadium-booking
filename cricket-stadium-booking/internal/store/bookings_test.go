package store

import (
	"context"
	"testing"
	"time"
)

func TestListBookings_BuyerScopedAndExcludesDiscardedHolds(t *testing.T) {
	s, matchID := testStore(t)
	ctx := context.Background()

	confirm := func(seatID, buyerID string) BookingRow {
		t.Helper()
		hold, err := s.PlaceHold(ctx, matchID, seatID, buyerID, 5*time.Minute)
		if err != nil {
			t.Fatalf("PlaceHold(%s, %s): %v", seatID, buyerID, err)
		}
		confirmed, err := s.ConfirmHold(ctx, hold.ID, buyerID)
		if err != nil {
			t.Fatalf("ConfirmHold(%s, %s): %v", seatID, buyerID, err)
		}
		return confirmed
	}

	cancelled := confirm("A1", "alice@example.com")
	if _, err := s.CancelBooking(ctx, cancelled.ID, "alice@example.com"); err != nil {
		t.Fatalf("CancelBooking: %v", err)
	}
	confirmed := confirm("A2", "alice@example.com")
	bob := confirm("A3", "bob@example.com")

	// A voluntary release is represented as status='cancelled' too, but it
	// was never a purchase and must not appear in booking history.
	discarded, err := s.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold discarded: %v", err)
	}
	if _, err := s.ReleaseHold(ctx, discarded.ID, "alice@example.com"); err != nil {
		t.Fatalf("ReleaseHold discarded: %v", err)
	}

	rows, err := s.ListBookings(ctx, matchID, "alice@example.com")
	if err != nil {
		t.Fatalf("ListBookings(alice): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("alice bookings = %+v, want exactly confirmed and cancelled purchases", rows)
	}

	byID := make(map[int64]BookingSummaryRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
		if row.MatchID != matchID {
			t.Errorf("row match_id = %q, want %q", row.MatchID, matchID)
		}
	}

	cancelledRow, ok := byID[cancelled.ID]
	if !ok {
		t.Fatalf("cancelled booking %d missing: %+v", cancelled.ID, rows)
	}
	if cancelledRow.Status != "cancelled" || cancelledRow.ConfirmedAt == nil || cancelledRow.CancelledAt == nil {
		t.Errorf("cancelled row = %+v, want confirmed_at and cancelled_at", cancelledRow)
	}
	if cancelledRow.RefundStatus == nil || *cancelledRow.RefundStatus != "pending" {
		t.Errorf("cancelled refund status = %v, want pending", cancelledRow.RefundStatus)
	}

	confirmedRow, ok := byID[confirmed.ID]
	if !ok {
		t.Fatalf("confirmed booking %d missing: %+v", confirmed.ID, rows)
	}
	if confirmedRow.Status != "confirmed" || confirmedRow.ConfirmedAt == nil || confirmedRow.CancelledAt != nil || confirmedRow.RefundStatus != nil {
		t.Errorf("confirmed row = %+v, want confirmed with no cancellation/refund", confirmedRow)
	}

	bobRows, err := s.ListBookings(ctx, matchID, "bob@example.com")
	if err != nil {
		t.Fatalf("ListBookings(bob): %v", err)
	}
	if len(bobRows) != 1 || bobRows[0].ID != bob.ID {
		t.Errorf("bob bookings = %+v, want only booking %d", bobRows, bob.ID)
	}

	missingRows, err := s.ListBookings(ctx, matchID, "mallory@example.com")
	if err != nil {
		t.Fatalf("ListBookings(mallory): %v", err)
	}
	if len(missingRows) != 0 {
		t.Errorf("mallory bookings = %+v, want empty", missingRows)
	}
}
