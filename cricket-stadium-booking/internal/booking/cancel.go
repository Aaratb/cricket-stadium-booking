package booking

import "context"

// CancelBooking never blocks on the payment gateway — the refund side
// effect is durably recorded via the outbox and completes asynchronously.
// See ADR-002.
func (svc *Service) CancelBooking(ctx context.Context, bookingID int64, buyerID string) (Booking, error) {
	ctx, cancel := svc.withDeadline(ctx)
	defer cancel()

	return withRetry(ctx, svc.maxRetries, func() (Booking, error) {
		r, err := svc.store.CancelBooking(ctx, bookingID, buyerID)
		if err != nil {
			return Booking{}, err
		}
		return Booking{ID: r.ID, SeatID: r.SeatID, Status: r.Status}, nil
	})
}
