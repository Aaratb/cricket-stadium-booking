package booking

import "context"

func (svc *Service) ConfirmHold(ctx context.Context, holdID int64, buyerID string) (Booking, error) {
	ctx, cancel := svc.withDeadline(ctx)
	defer cancel()

	b, err := withRetry(ctx, svc.maxRetries, func() (Booking, error) {
		r, err := svc.store.ConfirmHold(ctx, holdID, buyerID)
		if err != nil {
			return Booking{}, err
		}
		return Booking{ID: r.ID, MatchID: r.MatchID, SeatID: r.SeatID, Status: r.Status, ConfirmedAt: r.ConfirmedAt}, nil
	})
	if err == nil {
		// The seat's status changed; drop the cached seat map so the next
		// read reflects it (same contract as placeHold's invalidation).
		svc.seatCache.invalidate(b.MatchID)
	}
	return b, err
}
