package booking

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stadiumbooking/internal/store"
)

func TestServiceListBookings_MapsCancellationAndRefund(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres:///cricket_stadium_booking?host=/tmp")
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)

	matchID := fmt.Sprintf("booking-service-%s-%d", t.Name(), time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `INSERT INTO matches (id, name, start_time) VALUES ($1, 'test match', now() + interval '7 days')`, matchID); err != nil {
		t.Fatalf("seed match: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO seats (match_id, seat_id, section) VALUES ($1, 'A1', 'TEST')`, matchID); err != nil {
		t.Fatalf("seed seat: %v", err)
	}

	st := store.New(pool)
	svc := NewService(st, 5*time.Minute, 2*time.Second, 3)
	hold, err := st.PlaceHold(ctx, matchID, "A1", "alice@example.com", 5*time.Minute)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}
	if _, err := st.ConfirmHold(ctx, hold.ID, "alice@example.com"); err != nil {
		t.Fatalf("ConfirmHold: %v", err)
	}
	if _, err := st.CancelBooking(ctx, hold.ID, "alice@example.com"); err != nil {
		t.Fatalf("CancelBooking: %v", err)
	}

	bookings, err := svc.ListBookings(ctx, matchID, "alice@example.com")
	if err != nil {
		t.Fatalf("ListBookings: %v", err)
	}
	if len(bookings) != 1 {
		t.Fatalf("bookings = %+v, want one", bookings)
	}
	got := bookings[0]
	if got.ID != hold.ID || got.MatchID != matchID || got.SeatID != "A1" || got.Status != "cancelled" {
		t.Errorf("booking = %+v, want cancelled booking %d for %s/A1", got, hold.ID, matchID)
	}
	if got.ConfirmedAt == nil || got.CancelledAt == nil {
		t.Errorf("timestamps = confirmed %v cancelled %v, want both populated", got.ConfirmedAt, got.CancelledAt)
	}
	if got.RefundStatus == nil || *got.RefundStatus != "pending" {
		t.Errorf("refund status = %v, want pending", got.RefundStatus)
	}
}
