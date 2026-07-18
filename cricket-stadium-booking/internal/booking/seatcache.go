package booking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
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
	version   string
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

func (c *seatCache) get(matchID string) (seatCacheEntry, bool) {
	c.mu.RLock()
	e, ok := c.items[matchID]
	c.mu.RUnlock()
	if !ok || c.now().After(e.expiresAt) {
		return seatCacheEntry{}, false
	}
	return e, true
}

func (c *seatCache) put(matchID string, seats []Seat) seatCacheEntry {
	entry := seatCacheEntry{
		seats:     seats,
		version:   seatSnapshotVersion(matchID, seats),
		expiresAt: c.now().Add(c.ttl),
	}
	c.mu.Lock()
	c.items[matchID] = entry
	c.mu.Unlock()
	return entry
}

// seatSnapshotVersion is an opaque, deterministic validator for the complete
// public seat representation. It is computed once when a cache entry is
// populated and then shared by every waiter, so conditional polling does not
// hash all seats independently for every request. Length-prefixing strings
// prevents ambiguous concatenations from producing the same input stream.
func seatSnapshotVersion(matchID string, seats []Seat) string {
	h := sha256.New()
	writeVersionString := func(value string) {
		_, _ = h.Write([]byte(strconv.Itoa(len(value))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(value))
	}

	writeVersionString(matchID)
	for _, seat := range seats {
		writeVersionString(seat.SeatID)
		writeVersionString(seat.Section)
		writeVersionString(seat.Status)
		if seat.HoldExpiresAt == nil {
			writeVersionString("")
			continue
		}
		writeVersionString(seat.HoldExpiresAt.UTC().Format(time.RFC3339Nano))
	}
	return hex.EncodeToString(h.Sum(nil))
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
func (c *seatCache) load(ctx context.Context, matchID string, loader func() ([]Seat, error)) (seatCacheEntry, error) {
	if entry, ok := c.get(matchID); ok {
		return entry, nil
	}
	ch := c.group.DoChan(matchID, func() (any, error) {
		// Re-check inside the flight: a concurrent caller may have populated
		// the cache while we were queued behind it.
		if entry, ok := c.get(matchID); ok {
			return entry, nil
		}
		seats, err := loader()
		if err != nil {
			return nil, err
		}
		return c.put(matchID, seats), nil
	})
	select {
	case <-ctx.Done():
		return seatCacheEntry{}, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return seatCacheEntry{}, res.Err
		}
		return res.Val.(seatCacheEntry), nil
	}
}
