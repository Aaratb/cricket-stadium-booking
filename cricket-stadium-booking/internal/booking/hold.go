package booking

import "context"

func (svc *Service) PlaceHold(ctx context.Context, matchID, seatID, buyerID string) (Hold, error) {
	return svc.placeHold(ctx, matchID, seatID, buyerID, "")
}

// PlaceHoldWithKey is PlaceHold with a client idempotency key (empty key ==
// PlaceHold). A retry carrying the same key and the same match/seat/buyer
// returns the original hold while it is still live; the same key with
// different parameters fails with ErrIdempotencyKeyReuse, and a key whose
// hold has since expired or been cancelled lets the retry proceed fresh.
func (svc *Service) PlaceHoldWithKey(ctx context.Context, matchID, seatID, buyerID, idempotencyKey string) (Hold, error) {
	return svc.placeHold(ctx, matchID, seatID, buyerID, idempotencyKey)
}

func (svc *Service) placeHold(ctx context.Context, matchID, seatID, buyerID, idempotencyKey string) (Hold, error) {
	ctx, cancel := svc.withDeadline(ctx)
	defer cancel()

	row, err := withRetry(ctx, svc.maxRetries, func() (Hold, error) {
		r, err := svc.store.PlaceHoldWithKey(ctx, matchID, seatID, buyerID, svc.holdTTL, idempotencyKey)
		if err != nil {
			return Hold{}, err
		}
		return Hold{
			ID: r.ID, MatchID: r.MatchID, SeatID: r.SeatID, BuyerID: r.BuyerID,
			Status: r.Status, HoldExpiresAt: r.HoldExpiresAt,
		}, nil
	})
	if err == nil {
		// A new (or replayed) hold changes this match's seat map; drop the
		// cached view so the next read reflects it. Best-effort: a load
		// already in flight can still repopulate the cache with pre-commit
		// state, bounded by one seatCacheTTL window — within the cache's
		// accepted staleness envelope.
		svc.seatCache.invalidate(matchID)
	}
	return row, err
}

func (svc *Service) ReleaseHold(ctx context.Context, holdID int64, buyerID string) error {
	ctx, cancel := svc.withDeadline(ctx)
	defer cancel()
	matchID, err := svc.store.ReleaseHold(ctx, holdID, buyerID)
	if err != nil {
		return err
	}
	// The released seat is available again; drop the cached seat map so the
	// next read reflects it (same contract as placeHold's invalidation).
	svc.seatCache.invalidate(matchID)
	return nil
}
