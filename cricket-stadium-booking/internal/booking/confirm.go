package booking

import "context"

func (svc *Service) ConfirmHold(ctx context.Context, holdID int64, buyerID string) (Booking, error) {
	ctx, cancel := svc.withDeadline(ctx)
	defer cancel()

	return withRetry(ctx, svc.maxRetries, func() (Booking, error) {
		r, err := svc.store.ConfirmHold(ctx, holdID, buyerID)
		if err != nil {
			return Booking{}, err
		}
		return Booking{ID: r.ID, MatchID: r.MatchID, SeatID: r.SeatID, Status: r.Status, ConfirmedAt: r.ConfirmedAt}, nil
	})
}
