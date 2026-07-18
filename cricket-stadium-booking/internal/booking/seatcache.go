package booking

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// seatCacheTTL bounds how stale a seat map may be. ListSeats is already an
// eventually-consistent, derived-status read (ADR-001: a held-but-expired row
// reads as available with no sweeper involved), so a sub-second cache changes
// no correctness property — it only collapses a burst of identical reads
// (lakhs of clients polling one popular match) into one query per TTL window
// instead of one query per request against the primary.
const seatCacheTTL = 1 * time.Second

type seatCacheEntry struct {
	seats     []Seat
	expiresAt time.Time
}

// seatCache is a small read-through cache keyed by matchID. Concurrent misses
// for the same match are collapsed into a single loader call via singleflight,
// so a cache expiry under high read concurrency can't unleash a stampede of
// identical queries onto the primary.
type seatCache struct {
	ttl   time.Duration
	now   func() time.Time
	mu    sync.RWMutex
	items map[string]seatCacheEntry
	group singleflight.Group
}

func newSeatCache(ttl time.Duration) *seatCache {
	return &seatCache{ttl: ttl, now: time.Now, items: make(map[string]seatCacheEntry)}
}

func (c *seatCache) get(matchID string) ([]Seat, bool) {
	c.mu.RLock()
	e, ok := c.items[matchID]
	c.mu.RUnlock()
	if !ok || c.now().After(e.expiresAt) {
		return nil, false
	}
	return e.seats, true
}

func (c *seatCache) put(matchID string, seats []Seat) {
	c.mu.Lock()
	c.items[matchID] = seatCacheEntry{seats: seats, expiresAt: c.now().Add(c.ttl)}
	c.mu.Unlock()
}

// invalidate drops any cached seat map for matchID, so the next ListSeats
// re-reads live state. Called after a write that changes seat status, so a
// buyer who just grabbed a seat doesn't keep seeing it as available for up to
// a full TTL window.
func (c *seatCache) invalidate(matchID string) {
	c.mu.Lock()
	delete(c.items, matchID)
	c.mu.Unlock()
}

// load returns a fresh cache hit, or runs loader exactly once across all
// concurrent callers that miss on the same matchID. Callers must treat the
// returned slice as read-only (it is shared across concurrent readers).
//
// DoChan rather than Do, for two independent reasons: (1) each waiter keeps
// honoring its own ctx deadline instead of blocking as long as the flight
// runs, and (2) a waiter that gives up merely stops waiting — the shared
// flight keeps running (the loader carries its own detached context, see
// ListSeats) and its result still lands in the cache for the next reader.
func (c *seatCache) load(ctx context.Context, matchID string, loader func() ([]Seat, error)) ([]Seat, error) {
	if seats, ok := c.get(matchID); ok {
		return seats, nil
	}
	ch := c.group.DoChan(matchID, func() (any, error) {
		// Re-check inside the flight: a concurrent caller may have populated
		// the cache while we were queued behind it.
		if seats, ok := c.get(matchID); ok {
			return seats, nil
		}
		seats, err := loader()
		if err != nil {
			return nil, err
		}
		c.put(matchID, seats)
		return seats, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.([]Seat), nil
	}
}
