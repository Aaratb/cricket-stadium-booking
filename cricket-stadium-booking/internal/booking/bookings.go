package booking

import (
	"context"
	"time"
)

// BookingSummary is the stable buyer-facing representation used by the
// booking history UI. Nullable fields deliberately remain in the JSON as
// null so clients receive one predictable shape for confirmed and cancelled
// bookings.
type BookingSummary struct {
	ID           int64      `json:"booking_id,string"`
	MatchID      string     `json:"match_id"`
	SeatID       string     `json:"seat_id"`
	Status       string     `json:"status"`
	ConfirmedAt  *time.Time `json:"confirmed_at"`
	CancelledAt  *time.Time `json:"cancelled_at"`
	RefundStatus *string    `json:"refund_status"`
}

func (svc *Service) ListBookings(ctx context.Context, matchID, buyerID string) ([]BookingSummary, error) {
	ctx, cancel := svc.withDeadline(ctx)
	defer cancel()

	rows, err := svc.store.ListBookings(ctx, matchID, buyerID)
	if err != nil {
		return nil, err
	}
	out := make([]BookingSummary, len(rows))
	for i, row := range rows {
		out[i] = BookingSummary{
			ID:           row.ID,
			MatchID:      row.MatchID,
			SeatID:       row.SeatID,
			Status:       row.Status,
			ConfirmedAt:  row.ConfirmedAt,
			CancelledAt:  row.CancelledAt,
			RefundStatus: row.RefundStatus,
		}
	}
	return out, nil
}
