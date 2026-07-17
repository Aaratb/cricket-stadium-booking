package booking

import "context"

func (svc *Service) PlaceHold(ctx context.Context, matchID, seatID, buyerID string) (Hold, error) {
	ctx, cancel := svc.withDeadline(ctx)
	defer cancel()

	row, err := withRetry(ctx, svc.maxRetries, func() (Hold, error) {
		r, err := svc.store.PlaceHold(ctx, matchID, seatID, buyerID, svc.holdTTL)
		if err != nil {
			return Hold{}, err
		}
		return Hold{
			ID: r.ID, MatchID: r.MatchID, SeatID: r.SeatID, BuyerID: r.BuyerID,
			Status: r.Status, HoldExpiresAt: r.HoldExpiresAt,
		}, nil
	})
	return row, err
}

func (svc *Service) ReleaseHold(ctx context.Context, holdID int64, buyerID string) error {
	ctx, cancel := svc.withDeadline(ctx)
	defer cancel()
	return svc.store.ReleaseHold(ctx, holdID, buyerID)
}
